package main

import (
	"crypto/rand"
	"database/sql"
	"fmt"
	"io"
	"log"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"text/template"
	"time"

	_ "github.com/go-sql-driver/mysql"
	"github.com/google/uuid"
	"github.com/joho/godotenv"
	"golang.org/x/crypto/bcrypt"
	gomail "gopkg.in/gomail.v2"
)

// var and struct

var db *sql.DB
var err error
var tpl *template.Template
var sessions = map[string]session{}

// function de base

type session struct {
	userid int
	expiry time.Time
}

type File struct {
	ID           int        `db:"id"`
	UsersID      int        `db:"users_id"`
	FolderID     int        `db:"folder_id"`
	OriginalName string     `db:"original_name"`
	StoredPath   string     `db:"stored_path"`
	MimeType     string     `db:"mime_type"`
	SizeBytes    int64      `db:"size_bytes"`
	CreatedAt    time.Time  `db:"created_at"`
	DeletedAt    *time.Time `db:"deleted_at"`
}

type Folder struct {
	Id         int       `db:"id"`
	Users_id   int       `db:"users_id"`
	Nom        string    `db:"nom"`
	Parent_id  int       `db:"parent_id"`
	Created_at time.Time `db:"created_at"`
}

type PageData struct {
	Folders         []Folder
	Files           []File
	CurrentFolderID int
}

// SendEmail envoie un email générique.
func SendEmail(to, subject, body string) error {
	// Récupération de la config SMTP dans les variables d'environnement.
	smtpHost := os.Getenv("SMTP_HOST")
	smtpPortStr := os.Getenv("SMTP_PORT")
	smtpUser := os.Getenv("SMTP_USER")
	smtpPass := os.Getenv("SMTP_PASS")
	from := os.Getenv("SMTP_FROM")

	if smtpHost == "" || smtpPortStr == "" || smtpUser == "" || smtpPass == "" || from == "" {
		return fmt.Errorf("configuration SMTP manquante (vérifie SMTP_HOST, SMTP_PORT, SMTP_USER, SMTP_PASS, SMTP_FROM)")
	}

	// Conversion du port en int.
	var smtpPort int
	_, err := fmt.Sscanf(smtpPortStr, "%d", &smtpPort)
	if err != nil {
		return fmt.Errorf("SMTP_PORT invalide: %w", err)
	}

	// Construction du message.
	m := gomail.NewMessage()
	m.SetHeader("From", from)
	m.SetHeader("To", to)
	m.SetHeader("Subject", subject)
	m.SetBody("text/plain", body)

	// Dialer SMTP.
	d := gomail.NewDialer(smtpHost, smtpPort, smtpUser, smtpPass)

	// Envoi.
	if err := d.DialAndSend(m); err != nil {
		return fmt.Errorf("send email: %w", err)
	}

	return nil
}

func SendVerificationEmail(to, code string) error {
	subject := "Votre code de vérification"
	body := "Voici votre code : " + code + "\nIl expire dans 5 minutes."
	return SendEmail(to, subject, body)
}

func parseTemplates() (*template.Template, error) {
	tpl, err = template.ParseFiles(
		"../frontend/src/html/acceuil.html",
		"../frontend/src/html/login.html",
		"../frontend/src/html/register.html",
		"../frontend/src/html/verify.html",
		"../frontend/src/html/cloud_acceuil.html",
	)
	if err != nil {
		println("ERREUR func PARSETEMPLATES")
		return nil, err
	}

	return tpl, nil
}

func connectDB() (*sql.DB, error) {
	dsn := os.Getenv("DB_DSN")
	db, err = sql.Open("mysql", dsn)
	if err != nil {
		log.Fatal(err)
		return nil, err
	}
	if err = db.Ping(); err != nil {
		log.Fatal(err)
		return nil, err
	}
	println("Connecter a la db !")
	return db, nil
}

// cloud func

func NewUUIDv4() string {
	return uuid.NewString()
}

func uploadfilehandle(w http.ResponseWriter, r *http.Request) {
	var folder_id int
	var rootId int
	Struuid := NewUUIDv4()
	folder_idstr := r.URL.Query().Get("folder_id")
	Users_idstr := r.URL.Query().Get("Users_id")
	Users_id, err := strconv.Atoi(Users_idstr)
	if err != nil {
		http.Error(w, "convertion Users_id", http.StatusInternalServerError)
		return
	}

	if folder_idstr != "" {
		folder_id, err = strconv.Atoi(folder_idstr)
		if err != nil {
			http.Error(w, "convertion folder_id", http.StatusInternalServerError)
			return
		}
	} else {
		folder_id = 0
	}

	file, fileheader, err := r.FormFile("file")
	if err != nil {
		http.Error(w, "ERREUR dans la recuperation du fichier", http.StatusInternalServerError)
		return
	}

	defer file.Close()

	// Création du fichier vide

	if err = db.QueryRow("SELECT id FROM folders WHERE users_id = ? AND parent_id IS NULL AND nom = 'root'", &Users_id).Scan(&rootId); err == sql.ErrNoRows {
		root := "../storage/" + Users_idstr + "/" + "root"
		if err = os.MkdirAll(root, 0755); err != nil {
			http.Error(w, "ERREUR d'insert root", http.StatusInternalServerError)
			return
		}
		_, err = db.Exec("INSERT INTO folders (users_id, nom, parent_id) VALUES (?, 'root', NULL)", &Users_id)
		if err != nil {
			log.Fatal(err)
			http.Error(w, "ERREUR d'insert root", http.StatusInternalServerError)
			return
		}
	} else if err != nil {
		http.Error(w, "ERREUR de QueryROW", http.StatusInternalServerError)
		return
	}

	if err = db.QueryRow("SELECT id FROM folders WHERE users_id = ? AND parent_id IS NULL AND nom = 'root'", &Users_id).Scan(&rootId); err != nil {
		log.Fatal(err)
		http.Error(w, "ERREUR de QueryROW", http.StatusInternalServerError)
		return
	}

	if folder_id == 0 {
		folder_id = rootId
	}

	extension := filepath.Ext(fileheader.Filename)
	userDir := "../storage/" + Users_idstr
	os.MkdirAll(userDir, 0755)

	out, err := os.Create("../storage/" + Users_idstr + "/root/" + Struuid + extension)
	if err != nil {
		http.Error(w, "ERREUR dans la creation du fichier", http.StatusInternalServerError)
		return
	}

	defer out.Close()

	// Copie du contenu dans le fichier précédement vide
	ExactSizeBytes, err := io.Copy(out, file)
	if err != nil {
		http.Error(w, "ERREUR dans la copie du contenue", http.StatusInternalServerError)
		return
	}

	f := File{
		UsersID:      Users_id,
		FolderID:     folder_id,
		OriginalName: fileheader.Filename,
		StoredPath:   "storage/" + Users_idstr + "/root/" + Struuid + extension,
		MimeType:     fileheader.Header.Get("content-type"),
		SizeBytes:    ExactSizeBytes,
		CreatedAt:    time.Now(),
	}

	_, err = db.Exec("INSERT INTO files (users_id,folder_id,original_name,stored_path,mime_type,size_bytes,created_at) VALUES(?,?,?,?,?,?,?)",
		&f.UsersID, &f.FolderID, &f.OriginalName, &f.StoredPath, &f.MimeType, &f.SizeBytes, &f.CreatedAt)
	if err != nil {
		log.Fatal(err)
		//http.Error(w, "ERROR d'insert DB", http.StatusInternalServerError)
		return
	}
}

func downloadhandle(w http.ResponseWriter, r *http.Request) {
	file_idstr := r.URL.Query().Get("file_id")
	Users_idstr := r.URL.Query().Get("Users_id")
	Users_id, err := strconv.Atoi(Users_idstr)
	if err != nil {
		http.Error(w, "convertion Users_id", http.StatusInternalServerError)
		return
	}
	file_id, err := strconv.Atoi(file_idstr)
	if err != nil {
		http.Error(w, "convertion folder_id", http.StatusInternalServerError)
		return
	}
	var f File

	err = db.QueryRow("SELECT * FROM files WHERE users_id	 = ? AND id = ? ", &Users_id, &file_id).Scan(&f.ID, &f.UsersID, &f.FolderID, &f.OriginalName, &f.StoredPath, &f.MimeType, &f.SizeBytes, &f.CreatedAt, &f.DeletedAt)
	if err != nil {
		http.Error(w, "ERREUR 404 de select", http.StatusNotFound)
		return
	}

	storedFile, err := os.Open("../" + f.StoredPath)
	if err != nil {
		http.Error(w, "ERREUR dans l'ouverture du fichier", http.StatusInternalServerError)
		return
	}

	defer storedFile.Close()

	w.Header().Set("Content-Type", f.MimeType)
	SizeBytesstr := strconv.FormatInt(f.SizeBytes, 10)
	w.Header().Set("Content-Length", SizeBytesstr)
	disposition := "attachment; filename=\"" + f.OriginalName + "\""
	w.Header().Set("Content-Disposition", disposition)

	_, err = io.Copy(w, storedFile)
	if err != nil {
		http.Error(w, "ERREUR dans la copie des data", http.StatusInternalServerError)
		return
	}
}

func loadfolderhandle(w http.ResponseWriter, r *http.Request) ([]Folder, error) {
	var folder []Folder
	switch r.Method {
	case http.MethodGet:
		Users_idstr := r.URL.Query().Get("Users_id")
		Users_id, err := strconv.Atoi(Users_idstr)
		if err != nil {
			http.Error(w, "ERREOR convertion Users_id", http.StatusInternalServerError)
			return nil, err
		}

		rows, err := db.Query("SELECT id,	users_id,	nom, parent_id,	created_at FROM folders WHERE users_id = ?", &Users_id)
		if err != nil {
			http.Error(w, "ERROR de Select DB", http.StatusInternalServerError)
			return nil, err
		}

		defer rows.Close()

		for rows.Next() {
			var f Folder
			if err = rows.Scan(&f.Id, &f.Users_id, &f.Nom, &f.Parent_id, &f.Created_at); err != nil {
				return nil, err
			}
			folder = append(folder, f)
		}
		return folder, nil

	default:
		http.Error(w, "Méthode non autorisée", http.StatusMethodNotAllowed)
		return nil, err
	}
}

func folderCreationhandle(w http.ResponseWriter, r *http.Request) {
	/*var folder_id int
	folder_idstr := r.URL.Query().Get("folder_id")
	Users_idstr := r.URL.Query().Get("Users_id")
	Users_id, err := strconv.Atoi(Users_idstr)
	if err != nil {
		http.Error(w, "convertion Users_id", http.StatusInternalServerError)
		return
	}
	if folder_idstr != "" {
		folder_id, err = strconv.Atoi(folder_idstr)
		if err != nil {
			http.Error(w, "convertion folder_id", http.StatusInternalServerError)
			return
		}
	} else {
		folder_id = 0
	} */
	if r.Method == http.MethodPost {
		Users_idstr := r.FormValue("Users_id")
		Users_id, err := strconv.Atoi(Users_idstr)
		if err != nil {
			http.Error(w, "convertion Users_id", http.StatusInternalServerError)
			return
		}
		folder_idstr := r.FormValue("folder_id")
		folder_id, err := strconv.Atoi(folder_idstr)
		if err != nil {
			http.Error(w, "convertion folder_id", http.StatusInternalServerError)
			return
		}

		name := r.FormValue("name")
		if name == "" {
			http.Error(w, "ERREUR name ne peut pas etre vide", http.StatusBadRequest)
			return
		}

		f := Folder{
			Users_id:  Users_id,
			Nom:       name,
			Parent_id: folder_id,
		}

		_, err = db.Exec(
			"INSERT INTO folders (users_id, nom, parent_id) VALUES (?,?,?)",
			&f.Users_id, &f.Nom, &f.Parent_id,
		)
		if err != nil {
			http.Error(w, "ERREUR d'insert ", http.StatusInternalServerError)
			return
		}
	}
}

func deleteFileHandle(w http.ResponseWriter, r *http.Request) {
	if r.Method == http.MethodPost {
		Users_idstr := r.URL.Query().Get("Users_id")
		Users_id, err := strconv.Atoi(Users_idstr)
		if err != nil {
			http.Error(w, "convertion Users_id", http.StatusInternalServerError)
			return
		}
		file_idstr := r.FormValue("file_id")
		file_id, err := strconv.Atoi(file_idstr)
		if err != nil {
			http.Error(w, "convertion files_id", http.StatusInternalServerError)
			return
		}

		_, err = db.Exec("UPDATE files SET deleted_at = NOW() WHERE id = ? AND users_id = ?", &file_id, &Users_id)
		if err != nil {
			http.Error(w, "ERREUR de Update", http.StatusInternalServerError)
			return
		}
		http.Redirect(w, r, "/", http.StatusSeeOther)
	}
	if r.Method == http.MethodGet {
		http.Error(w, "Méthode non autorisée", http.StatusMethodNotAllowed)
		return
	}
}

func CloudAcceuilHandle(w http.ResponseWriter, r *http.Request) {
	var folder []Folder
	var file []File
	var folder_id int
	var rootId int
	switch r.Method {
	case http.MethodGet:
		folder_idstr := r.URL.Query().Get("folder_id")
		Users_idstr := r.URL.Query().Get("Users_id")
		Users_id, err := strconv.Atoi(Users_idstr)
		if err != nil {
			http.Error(w, "convertion Users_id", http.StatusInternalServerError)
			return
		}
		if folder_idstr != "" {
			folder_id, err = strconv.Atoi(folder_idstr)
			if err != nil {
				http.Error(w, "convertion folder_id", http.StatusInternalServerError)
				return
			}
		} else {
			folder_id = 0
		}

		if err = db.QueryRow("SELECT id FROM folders WHERE users_id = ? AND parent_id IS NULL AND nom = 'root'", &Users_id).Scan(&rootId); err == sql.ErrNoRows {
			root := "../storage/" + Users_idstr + "/" + "root"
			if err = os.MkdirAll(root, 0755); err != nil {
				http.Error(w, "ERREUR d'insert root", http.StatusInternalServerError)
				return
			}
			_, err = db.Exec("INSERT INTO folders (users_id, nom, parent_id) VALUES (?, 'root', NULL)", &Users_id)
			if err != nil {
				log.Fatal(err)
				http.Error(w, "ERREUR d'insert root", http.StatusInternalServerError)
				return
			}
		} else if err != nil {
			http.Error(w, "ERREUR de QueryROW", http.StatusInternalServerError)
			return
		}

		if err = db.QueryRow("SELECT id FROM folders WHERE users_id = ? AND parent_id IS NULL AND nom = 'root'", &Users_id).Scan(&rootId); err != nil {
			log.Fatal(err)
			http.Error(w, "ERREUR de QueryROW", http.StatusInternalServerError)
			return
		}

		if folder_id == 0 {
			folder_id = rootId
		}

		rowsfolderracine, err := db.Query("SELECT * FROM folders WHERE users_id = ? AND parent_id = ?", &Users_id, &folder_id)
		if err != nil {
			http.Error(w, "ERREUR Select", http.StatusInternalServerError)
			return
		}
		defer rowsfolderracine.Close()

		for rowsfolderracine.Next() {
			var f Folder
			if err = rowsfolderracine.Scan(&f.Id, &f.Users_id, &f.Nom, &f.Parent_id, &f.Created_at); err != nil {
				http.Error(w, "ERREUR scan", http.StatusInternalServerError)
				return
			}
			folder = append(folder, f)
		}

		rowsfile, err := db.Query("SELECT * FROM files WHERE users_id = ? AND folder_id = ? AND deleted_at IS NULL", &Users_id, &folder_id)
		if err != nil {
			http.Error(w, "ERREUR Select", http.StatusInternalServerError)
			return
		}

		defer rowsfile.Close()

		for rowsfile.Next() {
			var f File
			if err = rowsfile.Scan(&f.ID, &f.UsersID, &f.FolderID, &f.OriginalName, &f.StoredPath, &f.MimeType, &f.SizeBytes, &f.CreatedAt, &f.DeletedAt); err != nil {
				http.Error(w, "ERREUR scan", http.StatusInternalServerError)
				return
			}
			file = append(file, f)
		}

		data := PageData{
			Folders:         folder,
			Files:           file,
			CurrentFolderID: folder_id,
		}
		if err = tpl.ExecuteTemplate(w, "cloud_acceuil.html", data); err != nil {
			http.Error(w, "ERREUR de template", http.StatusInternalServerError)
			return
		}
	default:
		http.Error(w, "Méthode non autorisée", http.StatusMethodNotAllowed)
		return
	}
}

func generateCode() (string, error) {
	codes := make([]byte, 6)
	if _, err := rand.Read(codes); err != nil {
		return "", err
	}

	for i := 0; i < 6; i++ {
		codes[i] = uint8(48 + (codes[i] % 10))
	}

	return string(codes), nil
}

// handlefunc

func loginhandle(w http.ResponseWriter, r *http.Request) {
	switch r.Method {
	case http.MethodGet:
		if err = tpl.ExecuteTemplate(w, "login.html", nil); err != nil {
			http.Error(w, "ERREUR de template", http.StatusInternalServerError)
			return
		}
	case http.MethodPost:
		var MDPdp string
		var User_id int
		email := r.FormValue("email")
		password := r.FormValue("password")

		rows, err := db.Query("SELECT password_hash, id FROM users WHERE email = ?", email)
		if err != nil {
			fmt.Println("erreur de select db", err)
			return
		}
		defer rows.Close()

		if rows.Next() == false {
			http.Error(w, "Mots de passe ou email incorrect", http.StatusUnauthorized)
			return
		} else {
			//var emaildb strin
			if err := rows.Scan(&MDPdp, &User_id); err != nil {
				log.Fatal(err)
			}
		}
		//fmt.Println(User_id)

		if err := bcrypt.CompareHashAndPassword([]byte(MDPdp), []byte(password)); err != nil {
			http.Error(w, "Mots de passe ou email incorrect", http.StatusUnauthorized)
			return
		}

		Code, err := generateCode()
		if err != nil {
			fmt.Println("erreur de generation de code")
			return
		}

		expiresAt := time.Now().Add(5 * time.Minute)

		err = SendVerificationEmail(email, Code)
		if err != nil {
			fmt.Println("erreur dans l'envoie du mail")
			return
		}

		_, err = db.Exec("INSERT INTO email_verification(users_id, verify_token, verify_expires_at, is_verified) VALUES (?,?,?,0)", User_id, Code, expiresAt)
		if err != nil {
			http.Error(w, "Probeme de l'insertion du tokken verification", http.StatusInternalServerError)
			return
		}

		http.Redirect(w, r, "/verify?Users_id="+strconv.Itoa(User_id), http.StatusSeeOther)
		return
	default:
		http.Error(w, "Méthode non autorisée", http.StatusMethodNotAllowed)
		return
	}
}

func verifyhandle(w http.ResponseWriter, r *http.Request) {
	if r.Method == http.MethodGet {
		User_id_str := r.URL.Query().Get("Users_id")
		userid, err := strconv.Atoi(User_id_str)
		if err != nil || userid <= 0 {
			http.Error(w, "err de convertion string", http.StatusBadRequest)
			return
		}
		err = tpl.ExecuteTemplate(w, "verify.html", userid)
		if err != nil {
			http.Error(w, "Erreur de chargement login.html", http.StatusInternalServerError)
		}
	}
	if r.Method == http.MethodPost {
		code := r.FormValue("code")
		User_id := r.FormValue("Users_id")
		User_id_int, err := strconv.Atoi(User_id)
		if err != nil {
			http.Error(w, "err de convertion string", http.StatusBadRequest)
			return
		}

		var verify_token string
		var verify_expires_at time.Time
		var is_verified int

		err = db.QueryRow("SELECT verify_token, verify_expires_at, is_verified FROM email_verification WHERE users_id = ? ORDER BY id DESC LIMIT 1", User_id).Scan(&verify_token, &verify_expires_at, &is_verified)
		if err != nil {
			http.Error(w, "ERREUR de select db", http.StatusInternalServerError)
			return
		}
		if is_verified != 0 || time.Now().After(verify_expires_at) || code != verify_token {
			http.Error(w, "Code invalide ou expiré", http.StatusUnauthorized)
			tpl, _ := parseTemplates()
			tpl.ExecuteTemplate(w, "verify.html", nil)
			return
		}

		_, err = db.Exec("UPDATE email_verification SET is_verified = 1 WHERE verify_token = ? AND users_id = ?", verify_token, User_id)
		if err != nil {
			http.Error(w, "SERVEUR ERREUR", http.StatusInternalServerError)
			return
		}

		// we use the "github.com/google/uuid" library to generate UUIDs
		sessionToken := uuid.NewString()
		expiresAt := time.Now().Add(8 * time.Hour)

		sessions[sessionToken] = session{
			userid: User_id_int,
			expiry: expiresAt,
		}

		http.SetCookie(w, &http.Cookie{
			Name:    "session_token",
			Value:   sessionToken,
			Expires: expiresAt,
		})
		http.Redirect(w, r, "/cloud/acceuil/?Users_id="+User_id, http.StatusSeeOther)
		return
	}
}

func acceuilHandle(w http.ResponseWriter, r *http.Request) {
	switch r.Method {
	case http.MethodGet:
		if err = tpl.ExecuteTemplate(w, "acceuil.html", nil); err != nil {
			http.Error(w, "ERREUR de template", http.StatusInternalServerError)
			return
		}
	default:
		http.Error(w, "Méthode non autorisée", http.StatusMethodNotAllowed)
		return
	}

}

func registerhandle(w http.ResponseWriter, r *http.Request) {
	switch r.Method {
	case http.MethodGet:
		if err = tpl.ExecuteTemplate(w, "register.html", nil); err != nil {
			http.Error(w, "ERREUR de template", http.StatusInternalServerError)
			return
		}
	case http.MethodPost:
		email := r.FormValue("email")
		name := r.FormValue("name")
		password := r.FormValue("password")
		passwordV := r.FormValue("passwordV")

		if password != passwordV {
			http.Redirect(w, r, "/register?error=pass", http.StatusSeeOther)
			return
		}

		hashed, err := bcrypt.GenerateFromPassword([]byte(password), bcrypt.DefaultCost)
		if err != nil {
			http.Error(w, "erreur dans le hashed du MDP", http.StatusInternalServerError)
			return
		}

		_, err = db.Exec("INSERT INTO users (email, password_hash, name) VALUES (?,?,?)", email, hashed, name)
		if err != nil {
			http.Error(w, "erreur d'insert db", http.StatusInternalServerError)
			return
		}

		http.Redirect(w, r, "login.html", http.StatusSeeOther)

	default:
		http.Error(w, "Méthode non autorisée", http.StatusMethodNotAllowed)
		return
	}
}

func Logouthandle(w http.ResponseWriter, r *http.Request) {
	c, err := r.Cookie("session_token")
	if err != nil {
		if err == http.ErrNoCookie {
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		w.WriteHeader(http.StatusBadRequest)
		return
	}
	sessionToken := c.Value

	delete(sessions, sessionToken)

	http.SetCookie(w, &http.Cookie{
		Name:    "session_token",
		Value:   "",
		Expires: time.Now(),
	})

	http.Redirect(w, r, "login.html", http.StatusSeeOther)
}

// main
func main() {
	// chargement .env
	err = godotenv.Load(".env")
	if err != nil {
		println(".ENV ERROR")
	}

	// parseTemplates
	tpl, err = parseTemplates()
	if err != nil {
		println("TEMPLATE ERROR CONNECTION")
	}

	// connection db
	db, err = connectDB()
	if err != nil {
		println("DB ERROR CONNECTION")
	}
	defer db.Close()

	// router web
	http.HandleFunc("/", acceuilHandle)
	http.HandleFunc("/login", loginhandle)
	http.HandleFunc("/register", registerhandle)
	http.HandleFunc("/verify", verifyhandle)
	http.HandleFunc("/cloud/acceuil/", CloudAcceuilHandle)
	http.HandleFunc("/logout", Logouthandle)
	http.HandleFunc("/upload", uploadfilehandle)
	http.HandleFunc("/createfolder", folderCreationhandle)
	http.HandleFunc("/deletefile", deleteFileHandle)

	log.Println("Serveur sur http://localhost:8080")
	http.ListenAndServe(":8080", nil)

}
