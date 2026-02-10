package main

import (
	"archive/zip"
	"bytes"
	"crypto/rand"
	"database/sql"
	"fmt"
	"io"
	"log"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"
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
	Id         int        `db:"id"`
	Users_id   int        `db:"users_id"`
	Nom        string     `db:"nom"`
	Parent_id  int        `db:"parent_id"`
	Created_at time.Time  `db:"created_at"`
	DeletedAt  *time.Time `db:"deleted_at"`
}

type PageData struct {
	Folders         []Folder
	Files           []File
	CurrentFolderID int
	Crumb           []Crumbs
	ErrorMsg        string
	ErrorFileID     int
	ErrorFolderID   int
	MoveType        string // "file" ou "folder"
	MoveID          int

	Users_id   int
	CodeVerify string
}

type Crumbs struct {
	Id       int    `db:"id"`
	ParentID int    `db:"parent_id"`
	Name     string `db:"nom"`
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
		"../frontend/src/html/cloud_corbeille.html",
		"../frontend/src/html/harddeleteall.html",
		"../frontend/src/html/cloud_move.html",
		"../frontend/src/svg/folder.html",
		"../frontend/src/svg/file.html",
		"../frontend/src/svg/add.html",
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
	/*folder_idstr := r.URL.Query().Get("folder_id")
	Users_idstr := r.URL.Query().Get("Users_id")
	Users_id, err := strconv.Atoi(Users_idstr)
	if err != nil {
		http.Error(w, "convertion Users_id", http.StatusInternalServerError)
		return
	}*/
	c, err := r.Cookie("session_token")
	if err != nil { /* pas connecté */
	}

	sess, ok := sessions[c.Value]
	if !ok || time.Now().After(sess.expiry) { /* session invalide */
	}

	Users_id := sess.userid
	Users_idstr := strconv.Itoa(Users_id)

	folder_idstr := r.FormValue("folder_id")
	folder_id, err = strconv.Atoi(folder_idstr)
	if err != nil {
		http.Error(w, "convertion folder_id", http.StatusInternalServerError)
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
		http.Error(w, "ERROR d'insert DB", http.StatusInternalServerError)
		return
	}
	http.Redirect(w, r, "/cloud/acceuil/", http.StatusSeeOther)
}

func downloadhandle(w http.ResponseWriter, r *http.Request) {
	if r.Method == http.MethodPost {
		file_idstr := r.FormValue("file_id")
		file_id, err := strconv.Atoi(file_idstr)
		if err != nil {
			http.Error(w, "convertion folder_id", http.StatusInternalServerError)
			return
		}

		c, err := r.Cookie("session_token")
		if err != nil {
			http.Error(w, "Non authentifié", http.StatusUnauthorized)
			return
		}

		sess, ok := sessions[c.Value]
		if !ok || time.Now().After(sess.expiry) {
			http.Error(w, "Session expirée", http.StatusUnauthorized)
			return
		}

		Users_id := sess.userid
		var f File

		err = db.QueryRow("SELECT * FROM files WHERE users_id	 = ? AND id = ? AND deleted_at IS NULL", &Users_id, &file_id).Scan(&f.ID, &f.UsersID, &f.FolderID, &f.OriginalName, &f.StoredPath, &f.MimeType, &f.SizeBytes, &f.CreatedAt, &f.DeletedAt)
		if err != nil {
			if err == sql.ErrNoRows {
				http.Error(w, "Fichier introuvable ou supprimé", http.StatusNotFound)
				return
			}
			http.Error(w, "ERREUR 500 de select", http.StatusInternalServerError)
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

		http.Redirect(w, r, "/cloud/acceuil/", http.StatusSeeOther)
	} else {
		http.Error(w, "Méthode non autorisée", http.StatusMethodNotAllowed)
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
	var folder_id int
	/*folder_idstr := r.URL.Query().Get("folder_id")
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

	c, err := r.Cookie("session_token")
	if err != nil {
		http.Error(w, "Non authentifié", http.StatusUnauthorized)
		return
	}

	sess, ok := sessions[c.Value]
	if !ok || time.Now().After(sess.expiry) {
		http.Error(w, "Session expirée", http.StatusUnauthorized)
		return
	}

	Users_id := sess.userid
	//Users_idstr := strconv.Itoa(Users_id)

	if r.Method == http.MethodPost {
		folder_idstr := r.FormValue("folder_id")
		folder_id, err = strconv.Atoi(folder_idstr)
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
			log.Fatal(err)
			http.Error(w, "ERREUR d'insert ", http.StatusInternalServerError)
			return
		}
		http.Redirect(w, r, "/cloud/acceuil/", http.StatusSeeOther)
	}
}

func softdeleteFileHandle(w http.ResponseWriter, r *http.Request) {
	if r.Method == http.MethodPost {
		c, err := r.Cookie("session_token")
		if err != nil {
			http.Error(w, "Non authentifié", http.StatusUnauthorized)
			return
		}

		sess, ok := sessions[c.Value]
		if !ok || time.Now().After(sess.expiry) {
			http.Error(w, "Session expirée", http.StatusUnauthorized)
			return
		}

		Users_id := sess.userid

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

		http.Redirect(w, r, "/cloud/acceuil/", http.StatusSeeOther)
	}
	if r.Method == http.MethodGet {
		http.Error(w, "Méthode non autorisée", http.StatusMethodNotAllowed)
		return
	}
}

func softdeletefolderhandle(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "Méthode non autorisée", http.StatusMethodNotAllowed)
		return
	}

	c, err := r.Cookie("session_token")
	if err != nil {
		http.Error(w, "Non authentifié", http.StatusUnauthorized)
		return
	}

	sess, ok := sessions[c.Value]
	if !ok || time.Now().After(sess.expiry) {
		http.Error(w, "Session expirée", http.StatusUnauthorized)
		return
	}
	userID := sess.userid

	folderIDStr := r.FormValue("folder_id")
	folderID, err := strconv.Atoi(folderIDStr)
	if err != nil {
		http.Error(w, "convertion folder_id", http.StatusInternalServerError)
		return
	}

	// 1) récupérer tous les folder_id du sous-arbre
	folderIDs, err := collectFolderTreeIDs(userID, folderID)
	if err != nil {
		http.Error(w, "ERREUR collecte folders", http.StatusInternalServerError)
		return
	}

	// 2) soft delete les fichiers de tous ces dossiers
	if err := softDeleteFilesByFolderIDs(userID, folderIDs); err != nil {
		http.Error(w, "ERREUR soft delete files", http.StatusInternalServerError)
		return
	}

	// 3) soft delete tous les dossiers du sous-arbre
	if err := softDeleteFoldersByIDs(userID, folderIDs); err != nil {
		http.Error(w, "ERREUR soft delete folders", http.StatusInternalServerError)
		return
	}

	http.Redirect(w, r, "/cloud/acceuil/", http.StatusSeeOther)
}

// Met deleted_at = NOW() sur folders.id IN (ids...) pour un user donné.
func softDeleteFoldersByIDs(userID int, ids []int) error {
	if len(ids) == 0 {
		return nil
	}

	// construire "?,?,?,..."
	placeholders := make([]string, len(ids))
	for i := range placeholders {
		placeholders[i] = "?"
	}
	inClause := strings.Join(placeholders, ",")

	// args : d’abord userID, puis tous les ids
	args := make([]interface{}, 0, len(ids)+1)
	args = append(args, userID)
	for _, id := range ids {
		args = append(args, id)
	}

	query := "UPDATE folders SET deleted_at = NOW() WHERE users_id = ? AND id IN (" + inClause + ")"

	_, err := db.Exec(query, args...)
	return err
}

func restoreFoldersByIDs(userID int, ids []int) error {
	if len(ids) == 0 {
		return nil
	}

	// construire "?,?,?,..."
	placeholders := make([]string, len(ids))
	for i := range placeholders {
		placeholders[i] = "?"
	}
	inClause := strings.Join(placeholders, ",")

	// args : d’abord userID, puis tous les ids
	args := make([]interface{}, 0, len(ids)+1)
	args = append(args, userID)
	for _, id := range ids {
		args = append(args, id)
	}

	query := "UPDATE folders SET deleted_at = NULL WHERE users_id = ? AND id IN (" + inClause + ")"

	_, err := db.Exec(query, args...)
	return err
}

// Met deleted_at = NOW() sur files.folder_id IN (ids...) pour un user donné.
func softDeleteFilesByFolderIDs(userID int, folderIDs []int) error {
	if len(folderIDs) == 0 {
		return nil
	}

	placeholders := make([]string, len(folderIDs))
	for i := range placeholders {
		placeholders[i] = "?"
	}
	inClause := strings.Join(placeholders, ",")

	args := make([]interface{}, 0, len(folderIDs)+1)
	args = append(args, userID)
	for _, id := range folderIDs {
		args = append(args, id)
	}

	query := "UPDATE files SET deleted_at = NOW() WHERE users_id = ? AND folder_id IN (" + inClause + ")"

	_, err := db.Exec(query, args...)
	return err
}

func restoreFilesByFolderIDs(userID int, folderIDs []int) error {
	if len(folderIDs) == 0 {
		return nil
	}

	placeholders := make([]string, len(folderIDs))
	for i := range placeholders {
		placeholders[i] = "?"
	}
	inClause := strings.Join(placeholders, ",")

	args := make([]interface{}, 0, len(folderIDs)+1)
	args = append(args, userID)
	for _, id := range folderIDs {
		args = append(args, id)
	}

	query := "UPDATE files SET deleted_at = NULL WHERE users_id = ? AND folder_id IN (" + inClause + ")"

	_, err := db.Exec(query, args...)
	return err
}

// Retourne tous les dossiers à soft delete : le dossier de départ + tous ses enfants.
func collectFolderTreeIDs(userID, startFolderID int) ([]int, error) {
	// liste finale de tous les dossiers à supprimer
	var all []int

	// "queue" de dossiers à explorer (BFS)
	queue := []int{startFolderID}

	for len(queue) > 0 {
		// prendre le premier élément
		current := queue[0]
		queue = queue[1:] // enlever le premier

		all = append(all, current)

		// récupérer les enfants directs de current
		rows, err := db.Query(
			"SELECT id FROM folders WHERE users_id = ? AND parent_id = ?",
			userID, current,
		)
		if err != nil {
			return nil, err
		}
		defer rows.Close()

		for rows.Next() {
			var childID int
			if err := rows.Scan(&childID); err != nil {
				return nil, err
			}
			queue = append(queue, childID)
		}
	}

	return all, nil
}

func CloudAcceuilHandle(w http.ResponseWriter, r *http.Request) {
	var folder []Folder
	var file []File
	var folder_id int
	var rootId int
	switch r.Method {
	case http.MethodGet:
		/*Users_idstr := r.URL.Query().Get("Users_id")
		Users_id, err := strconv.Atoi(Users_idstr)
				if err != nil {
			http.Error(w, "convertion Users_id", http.StatusInternalServerError)
			return
		}*/

		Users_id, ok := requireSession(w, r)
		if !ok {
			return // redirigé vers /login
		}
		Users_idstr := strconv.Itoa(Users_id)

		folder_idstr := r.FormValue("folder_id")
		if folder_idstr != "" {
			folder_id, err = strconv.Atoi(folder_idstr)
			if err != nil {
				http.Error(w, "convertion folder_id", http.StatusInternalServerError)
				return
			}
		} else {
			folder_id = 0
		}

		if err = db.QueryRow("SELECT id FROM folders WHERE users_id = ? AND parent_id IS NULL AND nom = 'root' AND deleted_at IS NULL", &Users_id).Scan(&rootId); err == sql.ErrNoRows {
			root := "../storage/" + Users_idstr + "/" + "root"
			if err = os.MkdirAll(root, 0755); err != nil {
				http.Error(w, "ERREUR d'insert root", http.StatusInternalServerError)
				return
			}
			_, err = db.Exec("INSERT INTO folders (users_id, nom, parent_id) VALUES (?, 'root', NULL)", &Users_id)
			if err != nil {
				http.Error(w, "ERREUR d'insert root", http.StatusInternalServerError)
				return
			}
		} else if err != nil {
			log.Println("Pas de root trouvé, creation...")
			http.Error(w, "ERREUR de QueryROW", http.StatusInternalServerError)
			return
		}

		if err = db.QueryRow("SELECT id FROM folders WHERE users_id = ? AND parent_id IS NULL AND nom = 'root' AND deleted_at IS NULL", &Users_id).Scan(&rootId); err != nil {
			log.Println("Erreur QueryRow root:", err)
			http.Error(w, "ERREUR de QueryROW", http.StatusInternalServerError)
			return
		}

		if folder_id == 0 {
			folder_id = rootId
		}

		rowsfolder, err := db.Query("SELECT * FROM folders WHERE users_id = ? AND parent_id = ?  AND deleted_at IS NULL", &Users_id, &folder_id)
		if err != nil {
			http.Error(w, "ERREUR Select", http.StatusInternalServerError)
			return
		}
		defer rowsfolder.Close()

		for rowsfolder.Next() {
			var f Folder
			if err = rowsfolder.Scan(&f.Id, &f.Users_id, &f.Nom, &f.Parent_id, &f.Created_at, &f.DeletedAt); err != nil {
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

		listCrumb, err := rootbreadcrumb(folder_id, Users_id)
		if err != nil {
			http.Error(w, "Erreur listcrumb", http.StatusInternalServerError)
			return
		}

		errorMsg := r.URL.Query().Get("error")
		errorFileID := 0
		if errorFileIDStr := r.URL.Query().Get("error_file_id"); errorFileIDStr != "" {
			if v, err := strconv.Atoi(errorFileIDStr); err == nil {
				errorFileID = v
			}
		}
		errorFolderID := 0
		if errorFolderIDStr := r.URL.Query().Get("error_folder_id"); errorFolderIDStr != "" {
			if v, err := strconv.Atoi(errorFolderIDStr); err == nil {
				errorFolderID = v
			}
		}

		data := PageData{
			Folders:         folder,
			Files:           file,
			CurrentFolderID: folder_id,
			Crumb:           listCrumb,
			ErrorMsg:        errorMsg,
			ErrorFileID:     errorFileID,
			ErrorFolderID:   errorFolderID,
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

func CloudCorbeilleHandle(w http.ResponseWriter, r *http.Request) {
	var folder []Folder
	var file []File
	//var folder_id int
	//var rootId int
	switch r.Method {
	case http.MethodGet:
		Users_id, ok := requireSession(w, r)
		if !ok {
			return // redirigé vers /login
		}

		rowsfolder, err := db.Query("SELECT * FROM folders WHERE users_id = ? AND deleted_at IS NOT NULL", &Users_id)
		if err != nil {
			http.Error(w, "ERREUR Select", http.StatusInternalServerError)
			return
		}
		defer rowsfolder.Close()

		for rowsfolder.Next() {
			var f Folder
			if err = rowsfolder.Scan(&f.Id, &f.Users_id, &f.Nom, &f.Parent_id, &f.Created_at, &f.DeletedAt); err != nil {
				http.Error(w, "ERREUR scan", http.StatusInternalServerError)
				return
			}
			folder = append(folder, f)
		}

		rowsfile, err := db.Query("SELECT * FROM files WHERE users_id = ? AND deleted_at IS NOT NULL", &Users_id)
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

		/*listCrumb, err := rootbreadcrumb(folder_id, Users_id)
		if err != nil {
			http.Error(w, "Erreur listcrumb", http.StatusInternalServerError)
			return
		}*/
		data := PageData{
			Folders: folder,
			Files:   file,
			//CurrentFolderID: folder_id,
			//Crumb:           listCrumb,
		}

		if err = tpl.ExecuteTemplate(w, "cloud_corbeille.html", data); err != nil {
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

func rootbreadcrumb(idstart int, Users_id int) ([]Crumbs, error) {
	var crumbs []Crumbs
	var C Crumbs
	var parent sql.NullInt64
	for {
		if err = db.QueryRow("SELECT id, parent_id, nom FROM folders WHERE id = ? AND users_id = ?", &idstart, &Users_id).Scan(&C.Id, &parent, &C.Name); err != nil {
			return nil, err
		}
		crumbs = append(crumbs, C)
		if !parent.Valid {
			C.ParentID = 0
			break
		}
		C.ParentID = int(parent.Int64)
		idstart = C.ParentID
	}
	for i, j := 0, len(crumbs)-1; i < j; i, j = i+1, j-1 {
		crumbs[i], crumbs[j] = crumbs[j], crumbs[i]
	}
	return crumbs, nil
}

func renamedossier(w http.ResponseWriter, r *http.Request) {
	if r.Method == http.MethodPost {
		var folder_id int
		c, err := r.Cookie("session_token")
		if err != nil {
			http.Error(w, "Non authentifié", http.StatusUnauthorized)
			return
		}

		sess, ok := sessions[c.Value]
		if !ok || time.Now().After(sess.expiry) {
			http.Error(w, "Session expirée", http.StatusUnauthorized)
			return
		}

		Users_id := sess.userid

		folder_idstr := r.FormValue("folder_id")
		if folder_idstr != "" {
			folder_id, err = strconv.Atoi(folder_idstr)
			if err != nil {
				http.Error(w, "convertion folder_id", http.StatusInternalServerError)
				return
			}
		} else {
			folder_id = 0
		}

		newname := r.FormValue("newname")
		currentfolder_id := r.FormValue("currentfolderid")
		if len(newname) > 50 {
			http.Redirect(w, r, "/cloud/acceuil/?folder_id="+currentfolder_id+"&error=Nom de fichier trop long (max 50 caractères).&error_folder_id="+folder_idstr,
				http.StatusSeeOther)
			return
		}

		_, err = db.Exec("UPDATE folders SET nom = ? WHERE users_id = ? AND id = ? AND deleted_at IS NULL", &newname, &Users_id, &folder_id)
		if err != nil {
			http.Error(w, "ERREUR de update", http.StatusInternalServerError)
			return
		}

		http.Redirect(w, r, "/cloud/acceuil/?folder_id="+currentfolder_id, http.StatusSeeOther)
	} else {
		http.Error(w, "Methode non autoriser", http.StatusMethodNotAllowed)
		return
	}
}

func renameKeepAllExt(oldName, newBase string) string {
	// Extensions doubles courantes à préserver
	doubleExts := []string{".tar.gz", ".tar.bz2", ".tar.xz", ".tar.zst"}

	// Vérifier les extensions doubles
	for _, ext := range doubleExts {
		if strings.HasSuffix(oldName, ext) {
			return newBase + ext
		}
	}

	// Sinon, prendre uniquement la dernière extension (après le dernier point)
	dot := strings.LastIndex(oldName, ".")
	if dot == -1 {
		// pas d'extension
		return newBase
	}
	ext := oldName[dot:] // extension simple: ".png", ".jpg", etc.
	return newBase + ext
}

func renamefile(w http.ResponseWriter, r *http.Request) {
	if r.Method == http.MethodPost {
		var oldName string
		c, err := r.Cookie("session_token")
		if err != nil {
			http.Error(w, "Non authentifié", http.StatusUnauthorized)
			return
		}

		sess, ok := sessions[c.Value]
		if !ok || time.Now().After(sess.expiry) {
			http.Error(w, "Session expirée", http.StatusUnauthorized)
			return
		}

		Users_id := sess.userid

		file_idstr := r.FormValue("file_id")
		file_id, err := strconv.Atoi(file_idstr)
		if err != nil {
			http.Error(w, "convertion files_id", http.StatusInternalServerError)
			return
		}

		currentfolder_id := r.FormValue("currentfolderid")

		newname := r.FormValue("newname")
		if strings.Contains(newname, ".") {
			http.Redirect(w, r, "/cloud/acceuil/?folder_id="+currentfolder_id+"&error=Le Nom du fichier ne doit pas contenire de (.).&error_file_id="+file_idstr,
				http.StatusSeeOther)
			return
		}
		if len(newname) > 50 {
			http.Redirect(w, r, "/cloud/acceuil/?folder_id="+currentfolder_id+"&error=Nom de fichier trop long (max 50 caractères).&error_file_id="+file_idstr,
				http.StatusSeeOther)
			return
		}

		if err = db.QueryRow("SELECT original_name FROM files WHERE id = ? AND users_id = ? AND deleted_at IS NULL", &file_id, &Users_id).Scan(&oldName); err != nil {
			http.Error(w, "ERREUR de select original_name", http.StatusInternalServerError)
			return
		}

		finalName := renameKeepAllExt(oldName, newname)

		_, err = db.Exec("UPDATE files SET original_name = ? WHERE id = ? AND users_id = ? AND deleted_at IS NULL", &finalName, &file_id, &Users_id)
		if err != nil {
			http.Error(w, "ERREUR d'update", http.StatusInternalServerError)
			return
		}
		http.Redirect(w, r, "/cloud/acceuil/?folder_id="+currentfolder_id, http.StatusSeeOther)
	} else {
		http.Error(w, "Methode non autoriser", http.StatusMethodNotAllowed)
		return
	}
}

func HardDeleteAll(w http.ResponseWriter, r *http.Request) {
	var passwordDB string
	switch r.Method {
	case http.MethodGet:
		if err = tpl.ExecuteTemplate(w, "harddeleteall.html", nil); err != nil {
			http.Error(w, "ERREUR de template", http.StatusInternalServerError)
			return
		}
	case http.MethodPost:
		c, err := r.Cookie("session_token")
		if err != nil {
			http.Error(w, "Non authentifié", http.StatusUnauthorized)
			return
		}

		sess, ok := sessions[c.Value]
		if !ok || time.Now().After(sess.expiry) {
			http.Error(w, "Session expirée", http.StatusUnauthorized)
			return
		}

		Users_id := sess.userid

		password := r.FormValue("password")

		if err := db.QueryRow("SELECT password_hash FROM users WHERE id = ?", &Users_id).Scan(&passwordDB); err != nil {
			fmt.Println("erreur de select db", err)
			return
		}

		if err := bcrypt.CompareHashAndPassword([]byte(passwordDB), []byte(password)); err != nil {
			http.Error(w, "Mots de passe incorrect", http.StatusUnauthorized)
			return
		}

		rows, err := db.Query("SELECT stored_path FROM files WHERE deleted_at IS NOT NULL")
		if err != nil {
			http.Error(w, "erreur de select delete", http.StatusInternalServerError)
			return
		}

		defer rows.Close()

		for rows.Next() {
			var f File
			rows.Scan(&f.StoredPath)
			os.Remove("../" + f.StoredPath)
		}

		_, err = db.Exec("DELETE FROM folders WHERE users_id = ? AND deleted_at IS NOT NULL", &Users_id)
		if err != nil {
			http.Error(w, "ERREUR supresion des folders", http.StatusInternalServerError)
			return
		}
		_, err = db.Exec("DELETE FROM files WHERE users_id = ? AND deleted_at IS NOT NULL", &Users_id)
		if err != nil {
			http.Error(w, "ERREUR supresion des files", http.StatusInternalServerError)
			return
		}

		http.Redirect(w, r, "/cloud/corbeille/", http.StatusSeeOther)
	default:
		http.Error(w, "Méthode non autorisée", http.StatusMethodNotAllowed)
		return
	}
}

func harddeleteFolder(w http.ResponseWriter, r *http.Request) {
	if r.Method == http.MethodPost {
		c, err := r.Cookie("session_token")
		if err != nil {
			http.Error(w, "Non authentifié", http.StatusUnauthorized)
			return
		}

		sess, ok := sessions[c.Value]
		if !ok || time.Now().After(sess.expiry) {
			http.Error(w, "Session expirée", http.StatusUnauthorized)
			return
		}

		Users_id := sess.userid

		folder_idstr := r.FormValue("folder_id")
		folder_id, err := strconv.Atoi(folder_idstr)
		if err != nil {
			http.Error(w, "ERRERU convertion atoi", http.StatusInternalServerError)
			return
		}

		ListId, err := collectFolderTreeIDs(Users_id, folder_id)
		if err != nil {
			http.Error(w, "erreur collectFolderTreeIDs", http.StatusInternalServerError)
			return
		}

		placeholder := make([]string, len(ListId))
		for i := range placeholder {
			placeholder[i] = "?"
		}

		inClause := strings.Join(placeholder, ",")

		args := make([]interface{}, 0, len(ListId)+1)
		args = append(args, Users_id)
		for _, id := range ListId {
			args = append(args, id)
		}

		query := "SELECT stored_path FROM files WHERE deleted_at IS NOT NULL AND users_id = ? AND folder_id IN (" + inClause + ")"

		rows, err := db.Query(query, args...)
		if err != nil {
			http.Error(w, "erreur de select id", http.StatusInternalServerError)
			return
		}

		defer rows.Close()

		for rows.Next() {
			var f File
			rows.Scan(&f.StoredPath)
			os.Remove("../" + f.StoredPath)
		}

		_, err = db.Exec("DELETE FROM folders WHERE users_id = ? AND id = ? AND deleted_at IS NOT NULL", &Users_id, &folder_id)
		if err != nil {
			http.Error(w, "ERREUR supresion des folders", http.StatusInternalServerError)
			return
		}

		http.Redirect(w, r, "/cloud/corbeille/", http.StatusSeeOther)
	} else {
		http.Error(w, "Méthode non autorisée", http.StatusMethodNotAllowed)
		return
	}
}

func harddeleteFile(w http.ResponseWriter, r *http.Request) {
	var f File
	if r.Method == http.MethodPost {
		c, err := r.Cookie("session_token")
		if err != nil {
			http.Error(w, "Non authentifié", http.StatusUnauthorized)
			return
		}

		sess, ok := sessions[c.Value]
		if !ok || time.Now().After(sess.expiry) {
			http.Error(w, "Session expirée", http.StatusUnauthorized)
			return
		}

		Users_id := sess.userid

		file_idstr := r.FormValue("file_id")
		file_id, err := strconv.Atoi(file_idstr)
		if err != nil {
			http.Error(w, "ERREUR convertion atoi", http.StatusInternalServerError)
			return
		}

		if err = db.QueryRow("SELECT stored_path FROM files WHERE users_id = ? AND id = ? AND deleted_at IS NOT NULL", &Users_id, &file_id).Scan(&f.StoredPath); err != nil {
			http.Error(w, "ERREUR de select f.StoredPath", http.StatusInternalServerError)
			return
		}

		if err = os.Remove("../" + f.StoredPath); err != nil {
			http.Error(w, "ERREUR de delete du file dans le stoarge/", http.StatusInternalServerError)
			return
		}

		_, err = db.Exec("DELETE FROM files WHERE users_id = ? AND id = ? AND deleted_at IS NOT NULL", &Users_id, &file_id)
		if err != nil {
			http.Error(w, "ERREUR supresion des folders", http.StatusInternalServerError)
			return
		}

		http.Redirect(w, r, "/cloud/corbeille/", http.StatusSeeOther)
	} else {
		http.Error(w, "Méthode non autorisée", http.StatusMethodNotAllowed)
		return
	}
}

func restoreFolderChain(userID int, folderID int) {
	var parent sql.NullInt64
	var deletedAt sql.NullTime

	currentfolder_id := folderID
	for {
		err = db.QueryRow(
			"SELECT parent_id, deleted_at FROM folders WHERE users_id = ? AND id = ?",
			userID, currentfolder_id,
		).Scan(&parent, &deletedAt)
		if err != nil {
			fmt.Println("ERREUR select:", err)
			return
		}

		if deletedAt.Valid {
			_, err = db.Exec(
				"UPDATE folders SET deleted_at = NULL WHERE users_id = ? AND id = ?",
				userID, currentfolder_id,
			)
			if err != nil {
				fmt.Println("ERREUR update:", err)
				return
			}
		}

		if !parent.Valid {
			break
		}
		currentfolder_id = int(parent.Int64)
	}
}

func restorfile(w http.ResponseWriter, r *http.Request) {
	if r.Method == http.MethodPost {
		c, err := r.Cookie("session_token")
		if err != nil {
			http.Error(w, "Non authentifié", http.StatusUnauthorized)
			return
		}

		sess, ok := sessions[c.Value]
		if !ok || time.Now().After(sess.expiry) {
			http.Error(w, "Session expirée", http.StatusUnauthorized)
			return
		}

		Users_id := sess.userid

		file_idstr := r.FormValue("file_id")
		file_id, err := strconv.Atoi(file_idstr)

		var folderID int
		err = db.QueryRow(
			"SELECT folder_id FROM files WHERE users_id = ? AND id = ? AND deleted_at IS NOT NULL",
			Users_id, file_id,
		).Scan(&folderID)

		restoreFolderChain(Users_id, folderID)

		_, err = db.Exec(
			"UPDATE files SET deleted_at = NULL WHERE users_id = ? AND id = ? AND deleted_at IS NOT NULL",
			Users_id, file_id,
		)

		http.Redirect(w, r, "/cloud/corbeille/", http.StatusSeeOther)
	} else {
		http.Error(w, "Méthode non autoriser", http.StatusMethodNotAllowed)
		return
	}
}

func restorfolder(w http.ResponseWriter, r *http.Request) {
	var folder_id int
	c, err := r.Cookie("session_token")
	if err != nil {
		http.Error(w, "Non authentifié", http.StatusUnauthorized)
		return
	}

	sess, ok := sessions[c.Value]
	if !ok || time.Now().After(sess.expiry) {
		http.Error(w, "Session expirée", http.StatusUnauthorized)
		return
	}

	Users_id := sess.userid

	folder_idstr := r.FormValue("folder_id")
	if folder_idstr != "" {
		folder_id, err = strconv.Atoi(folder_idstr)
		if err != nil {
			http.Error(w, "convertion folder_id", http.StatusInternalServerError)
			return
		}
	} else {
		folder_id = 0
	}

	restoreFolderChain(Users_id, folder_id)

	// 2) tous les folders du sous-arbre
	folderIDs, err := collectFolderTreeIDs(Users_id, folder_id)
	if err != nil {
		http.Error(w, "ERREUR collecte folders", http.StatusInternalServerError)
		return
	}

	// 3) restore files de ces folders
	if err := restoreFilesByFolderIDs(Users_id, folderIDs); err != nil {
		http.Error(w, "ERREUR restore files", http.StatusInternalServerError)
		return
	}

	// 4) restore folders du sous-arbre (enfants + racine)
	if err := restoreFoldersByIDs(Users_id, folderIDs); err != nil {
		http.Error(w, "ERREUR restore folders", http.StatusInternalServerError)
		return
	}

	http.Redirect(w, r, "/cloud/corbeille/", http.StatusSeeOther)
}

func uploadFolder(w http.ResponseWriter, r *http.Request) {
	var rootid int
	Users_id, ok := requireSession(w, r)
	if !ok {
		return // redirigé vers /login
	}
	Users_idstr := strconv.Itoa(Users_id)

	// 1) Nom du dossier dans le cloud (tapé par l'utilisateur)
	cloudFolderName := r.FormValue("cloud_folder_name")
	if cloudFolderName == "" {
		cloudFolderName = "Dossier_sans_nom"
	}

	if err = db.QueryRow("SELECT id FROM folders WHERE users_id = ? AND nom = 'root' AND parent_id IS NULL AND deleted_at IS NULL", &Users_id).Scan(&rootid); err != nil {
		http.Error(w, "ERREUR de scan folder", http.StatusInternalServerError)
		return
	}

	// 2) Parse du multipart
	err = r.ParseMultipartForm(0)
	if err != nil {
		http.Error(w, "ERREUR de ParseMultipartForm", http.StatusInternalServerError)
		return
	}

	form := r.MultipartForm
	files := form.File["files"]
	paths := form.Value["paths"]

	// 3) Pour chaque fichier reçu
	for i, fh := range files {
		extension := filepath.Ext(fh.Filename)
		if extension == ".DS_Store" {
			continue
		}
		Struuid := NewUUIDv4()
		relPath := fh.Filename                     // ici tu as juste le nom du fichier
		name := filepath.Base(Struuid + extension) // ex: "Capture d’écran ....png"
		TimeNow := time.Now()

		virtualPath := paths[i] // ex: "testcloud/sous/file.png"
		fmt.Println("file:", fh.Filename, "virtual:", virtualPath)
		folderID, err := RootFolderupload(Users_id, rootid, virtualPath, TimeNow)
		if err != nil {
			http.Error(w, "erreur d'arbre dossier", http.StatusInternalServerError)
			return
		}

		rootstorage := "../storage/" + Users_idstr + "/root/"

		dstPath := filepath.Join(rootstorage, name)
		fmt.Println("sauvegarde vers :", dstPath)

		// 4) Ouverture du fichier envoyé
		src, err := fh.Open()
		if err != nil {
			http.Error(w, "ERREUR ouverture fichier", http.StatusInternalServerError)
			return
		}
		defer src.Close()

		// 5) Création du fichier côté serveur
		dst, err := os.Create(dstPath)
		if err != nil {
			http.Error(w, "ERREUR création fichier serveur", http.StatusInternalServerError)
			return
		}

		// 6) Copie des données
		ExactSizeBytes, err := io.Copy(dst, src)
		if err != nil {
			http.Error(w, "ERREUR copie fichier", http.StatusInternalServerError)
			return
		}
		dst.Close()

		f := File{
			UsersID:      Users_id,
			FolderID:     folderID,
			OriginalName: relPath,
			StoredPath:   "storage/" + Users_idstr + "/root/" + Struuid + extension,
			MimeType:     fh.Header.Get("content-type"),
			SizeBytes:    ExactSizeBytes,
			CreatedAt:    TimeNow,
		}

		// ici plus tard tu pourras faire un INSERT dans ta table files
		_, err = db.Exec("INSERT INTO files (users_id,folder_id,original_name,stored_path,mime_type,size_bytes,created_at) VALUES(?,?,?,?,?,?,?)", &f.UsersID, &f.FolderID, &f.OriginalName, &f.StoredPath, &f.MimeType, &f.SizeBytes, &f.CreatedAt)
		if err != nil {
			http.Error(w, "ERREUR de insert files", http.StatusInternalServerError)
			return
		}
	}

	currentfolder_idstr := r.FormValue("folder_id")

	// 7) Redirection après l’upload
	http.Redirect(w, r, "/cloud/acceuil/?folder_id="+currentfolder_idstr, http.StatusSeeOther)
}

func RootFolderupload(UserId int, RootId int, virtualPath string, TimeNow time.Time) (int, error) {
	parts := strings.Split(virtualPath, "/")
	if len(parts) <= 1 {
		return RootId, nil
	}

	currentParent := RootId

	dirs := parts[:len(parts)-1]
	for _, name := range dirs {
		if name == "" {
			continue
		}

		var id int
		if err = db.QueryRow("SELECT id FROM folders WHERE users_id = ? AND nom = ? AND deleted_at IS NULL AND parent_id = ?",
			&UserId, &name, &currentParent).Scan(&id); err == sql.ErrNoRows {
			folder := Folder{
				Nom:        name,
				Parent_id:  currentParent,
				Users_id:   UserId,
				Created_at: TimeNow,
			}
			res, err := db.Exec("INSERT INTO folders (users_id, nom, parent_id, created_at) VALUES (?,?,?,?)", &folder.Users_id, &folder.Nom, &folder.Parent_id, &folder.Created_at)
			if err != nil {
				return 0, err
			}

			newID, err := res.LastInsertId()
			if err != nil {
				return 0, err
			}
			id = int(newID)
		} else if err != nil {
			return 0, err
		}
		currentParent = id
	}

	return currentParent, nil
}

// handlefunc

func loginhandle(w http.ResponseWriter, r *http.Request) {
	data := PageData{}
	switch r.Method {
	case http.MethodGet:
		if msgerror := r.URL.Query().Get("error"); msgerror != "" {
			data.CodeVerify = "Mots de passe ou email incorrect"
		}
		if err = tpl.ExecuteTemplate(w, "login.html", data); err != nil {
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
			http.Redirect(w, r, "/login?error=code", http.StatusSeeOther)
			return
		} else {
			//var emaildb strin
			if err := rows.Scan(&MDPdp, &User_id); err != nil {
				log.Fatal(err)
			}
		}
		//fmt.Println(User_id)

		if err := bcrypt.CompareHashAndPassword([]byte(MDPdp), []byte(password)); err != nil {
			http.Redirect(w, r, "/login?error=code", http.StatusSeeOther)
			return
		}

		Code, err := generateCode()
		if err != nil {
			fmt.Println("erreur de generation de code")
			return
		}

		expiresAt := time.Now().Add(5 * time.Minute)

		println("voici le code : ", Code)
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
	switch r.Method {
	case http.MethodGet:
		User_id_str := r.URL.Query().Get("Users_id")
		userid, err := strconv.Atoi(User_id_str)
		if err != nil || userid <= 0 {
			http.Error(w, "err de convertion string", http.StatusBadRequest)
			return
		}

		data := PageData{Users_id: userid}

		if msgerror := r.URL.Query().Get("error"); msgerror != "" {
			data.CodeVerify = "Code invalide ou expiré"
		}

		if err := tpl.ExecuteTemplate(w, "verify.html", data); err != nil {
			http.Error(w, "Erreur de chargement verify.html", http.StatusInternalServerError)
			return
		}
		return

	case http.MethodPost:
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

		err = db.QueryRow(
			"SELECT verify_token, verify_expires_at, is_verified FROM email_verification WHERE users_id = ? ORDER BY id DESC LIMIT 1",
			User_id_int,
		).Scan(&verify_token, &verify_expires_at, &is_verified)
		if err != nil || is_verified != 0 || time.Now().After(verify_expires_at) || code != verify_token {
			http.Redirect(w, r, "/verify?Users_id="+User_id+"&error=code", http.StatusSeeOther)
			return
		}

		_, err = db.Exec(
			"UPDATE email_verification SET is_verified = 1 WHERE verify_token = ? AND users_id = ?",
			verify_token, User_id_int,
		)
		if err != nil {
			http.Error(w, "SERVEUR ERREUR", http.StatusInternalServerError)
			return
		}

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
		http.Redirect(w, r, "/cloud/acceuil/", http.StatusSeeOther)
		return

	default:
		http.Error(w, "Méthode non autorisée", http.StatusMethodNotAllowed)
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
	data := PageData{}
	switch r.Method {
	case http.MethodGet:
		msgerror := r.URL.Query().Get("error")
		switch msgerror {
		case "password":
			data.CodeVerify = "Mots de passes non indentique"
		case "email":
			data.CodeVerify = "email déja utiliser"
		default:
			data.CodeVerify = ""
		}
		if err = tpl.ExecuteTemplate(w, "register.html", data); err != nil {
			http.Error(w, "ERREUR de template", http.StatusInternalServerError)
			return
		}

	case http.MethodPost:
		email := r.FormValue("email")
		name := r.FormValue("name")
		password := r.FormValue("password")
		passwordV := r.FormValue("passwordV")

		if password != passwordV {
			http.Redirect(w, r, "/register?error=password", http.StatusSeeOther)
			return
		}

		rowsUsers, err := db.Query("SELECT email FROM users")
		if err != nil {
			http.Error(w, "erreur dans le email select", http.StatusInternalServerError)
			return
		}

		defer rowsUsers.Close()

		for rowsUsers.Next() {
			var emailverif string
			rowsUsers.Scan(&emailverif)
			if emailverif != email {
				continue
			} else {
				http.Redirect(w, r, "/register?error=email", http.StatusSeeOther)
				return
			}
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

func getAuthenticatedUserID(r *http.Request) (int, bool) {
	c, err := r.Cookie("session_token")
	if err != nil {
		return 0, false
	}

	sess, ok := sessions[c.Value]
	if !ok {
		return 0, false
	}

	if time.Now().After(sess.expiry) {
		delete(sessions, c.Value)
		return 0, false
	}

	return sess.userid, true
}

func requireSession(w http.ResponseWriter, r *http.Request) (int, bool) {
	userID, ok := getAuthenticatedUserID(r)
	if !ok {
		http.Redirect(w, r, "/login", http.StatusSeeOther)
		return 0, false
	}
	return userID, true
}

func downloadFolder(w http.ResponseWriter, r *http.Request) {
	var f []File
	Users_id, ok := requireSession(w, r)
	if r.Method != http.MethodPost {
		http.Error(w, "Méthode non autorisée", http.StatusMethodNotAllowed)
		return
	}

	if !ok {
		return // redirigé vers /login
	}
	//Users_idstr := strconv.Itoa(Users_id)

	Folder_idstr := r.FormValue("folder_id")
	Folder_id, err := strconv.Atoi(Folder_idstr)

	folder_namestr := r.FormValue("folder_namestr")
	if err != nil {
		http.Error(w, "erreur de convertion folder", http.StatusInternalServerError)
		return
	}
	ListId, err := collectFolderTreeIDs(Users_id, Folder_id)
	if err != nil {
		http.Error(w, "erreur de list d'id", http.StatusInternalServerError)
		return
	}
	fmt.Print(ListId)

	placeholder := make([]string, len(ListId))
	for i := range placeholder {
		placeholder[i] = "?"
	}

	inClause := strings.Join(placeholder, ",")

	args := make([]interface{}, 0, len(ListId)+1)
	args = append(args, Users_id)
	for _, id := range ListId {
		args = append(args, id)
	}

	query := "SELECT * FROM files WHERE deleted_at IS NULL AND users_id = ? AND folder_id IN(" + inClause + ")"

	rows, err := db.Query(query, args...)
	if err != nil {
		http.Error(w, "erreur de select", http.StatusInternalServerError)
		return
	}
	defer rows.Close()

	for rows.Next() {
		var file File
		rows.Scan(&file.ID, &file.UsersID, &file.FolderID, &file.OriginalName, &file.StoredPath, &file.MimeType, &file.SizeBytes, &file.CreatedAt, &file.DeletedAt)

		f = append(f, file)
	}

	buf := &bytes.Buffer{}
	zw := zip.NewWriter(buf)

	if err = CreateZip(zw, ListId, f, Users_id); err != nil {
		http.Error(w, "erreur createzip", http.StatusInternalServerError)
		return
	}

	if err = zw.Close(); err != nil {
		http.Error(w, "erreur dans la fermeture du zip", http.StatusInternalServerError)
		return
	}

	// headers HTTP pour le téléchargement
	w.Header().Set("Content-Type", "application/zip")
	w.Header().Set("Content-Disposition", `attachment; filename="`+folder_namestr+`.zip"`)
	w.Header().Set("Content-Length", strconv.Itoa(buf.Len()))

	if _, err := w.Write(buf.Bytes()); err != nil {
		log.Println(err)
	}
}

func CreateZip(zw *zip.Writer, ListId []int, file []File, User_id int) error {
	for _, id := range ListId {
		for _, currentfile := range file {
			if currentfile.FolderID != id {
				continue
			}
			root, err := rootbreadcrumb(id, User_id)
			if err != nil {
				return err
			}

			parts := make([]string, 0, len(root)-1)

			for i, namepart := range root {
				if i == 0 {
					continue
				}
				parts = append(parts, namepart.Name)
			}
			pathname := strings.Join(parts, "/")

			zipPath := pathname + "/" + currentfile.OriginalName

			fw, err := zw.Create(zipPath)
			if err != nil {
				return err
			}

			src, err := os.Open("../" + currentfile.StoredPath)
			if err != nil {
				return err
			}

			if _, err = io.Copy(fw, src); err != nil {
				src.Close()
				return err
			}
			src.Close()
		}
	}
	return nil
}

func moveHandle(w http.ResponseWriter, r *http.Request) {
	var folder_id int
	var rootId int
	var folder []Folder
	var file []File
	switch r.Method {
	case http.MethodGet:
		Users_id, ok := requireSession(w, r)
		if !ok {
			return // redirigé vers /login
		}
		//Users_idstr := strconv.Itoa(Users_id)

		folder_idstr := r.FormValue("folder_id")
		moveType := r.FormValue("type") // "file" ou "folder"
		moveIDStr := r.FormValue("id")
		moveID, _ := strconv.Atoi(moveIDStr)
		if folder_idstr != "" {
			folder_id, err = strconv.Atoi(folder_idstr)
			if err != nil {
				http.Error(w, "convertion folder_id", http.StatusInternalServerError)
				return
			}
		} else {
			folder_id = 0
		}

		if err = db.QueryRow("SELECT id FROM folders WHERE users_id = ? AND parent_id IS NULL AND nom = 'root' AND deleted_at IS NULL", &Users_id).Scan(&rootId); err != nil {
			log.Println("Erreur QueryRow root:", err)
			http.Error(w, "ERREUR de QueryROW", http.StatusInternalServerError)
			return
		}

		if folder_id == 0 {
			folder_id = rootId
		}

		rowsfolder, err := db.Query("SELECT * FROM folders WHERE users_id = ? AND parent_id = ?  AND deleted_at IS NULL", &Users_id, &folder_id)
		if err != nil {
			http.Error(w, "ERREUR Select", http.StatusInternalServerError)
			return
		}
		defer rowsfolder.Close()

		for rowsfolder.Next() {
			var f Folder
			if err = rowsfolder.Scan(&f.Id, &f.Users_id, &f.Nom, &f.Parent_id, &f.Created_at, &f.DeletedAt); err != nil {
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

		listCrumb, err := rootbreadcrumb(folder_id, Users_id)
		if err != nil {
			http.Error(w, "Erreur listcrumb", http.StatusInternalServerError)
			return
		}

		data := PageData{
			Folders:         folder,
			Files:           file,
			CurrentFolderID: folder_id,
			Crumb:           listCrumb,
			MoveType:        moveType,
			MoveID:          moveID,
		}

		msgerror := r.URL.Query().Get("error")
		switch msgerror {
		case "1":
			data.ErrorMsg = "Impossible de déplacer un dossier dans un dossier qui ne vous appartient pas"
		case "2":
			data.ErrorMsg = "Impossible de déplacer un dossier dans lui-même"
		case "3":
			data.ErrorMsg = "Impossible de déplacer un dossier dans un de ses sous-dossiers"
		default:
			data.ErrorMsg = ""
		}

		if err = tpl.ExecuteTemplate(w, "cloud_move.html", data); err != nil {
			http.Error(w, "ERREUR de template", http.StatusInternalServerError)
			return
		}

	case http.MethodPost:
		Users_id, ok := requireSession(w, r)
		if !ok {
			return
		}

		moveType := r.FormValue("type")
		idStr := r.FormValue("id")
		destStr := r.FormValue("destination_folder_id")

		id, err := strconv.Atoi(idStr)
		if err != nil { /* handle */
		}
		destID, err := strconv.Atoi(destStr)
		if err != nil {
			println(err)
		}

		var ListFolderid []int

		if ListFolderid, err = ListAllFolderByUsersId(Users_id); err != nil {
			http.Error(w, "erreur de function list", http.StatusBadRequest)
			return
		}

		// Vérifier si la destination appartient à l'utilisateur
		destBelongsToUser := false
		for _, folderID := range ListFolderid {
			if folderID == destID {
				destBelongsToUser = true
				break
			}
		}
		if !destBelongsToUser {
			http.Redirect(w, r, fmt.Sprintf("/cloud/replace/?type=%s&id=%d&error=1", moveType, id), http.StatusSeeOther)
			return
		}

		if moveType == "folder" && id == destID {
			http.Redirect(w, r, fmt.Sprintf("/cloud/replace/?type=%s&id=%d&error=2", moveType, id), http.StatusSeeOther)
			return
		}

		if moveType == "folder" {
			descendants, err := collectFolderTreeIDs(Users_id, id)
			if err != nil { /* handle */
			}

			for _, d := range descendants {
				if d == destID {
					http.Redirect(w, r, fmt.Sprintf("/cloud/replace/?type=%s&id=%d&error=3", moveType, id), http.StatusSeeOther)
					return
				}
			}
		}

		switch moveType {
		case "file":
			_, err = db.Exec(
				"UPDATE files SET folder_id = ? WHERE id = ? AND users_id = ?",
				destID, id, Users_id,
			)
		case "folder":
			_, err = db.Exec(
				"UPDATE folders SET parent_id = ? WHERE id = ? AND users_id = ?",
				destID, id, Users_id,
			)
		default:
			http.Error(w, "type invalide", http.StatusBadRequest)
			return
		}
		if err != nil {
			http.Error(w, "erreur move", http.StatusInternalServerError)
			return
		}

		http.Redirect(w, r, "/cloud/acceuil/", http.StatusSeeOther)

	default:
		http.Error(w, "Méthode non autorisée", http.StatusMethodNotAllowed)
		return
	}
}

func ListAllFolderByUsersId(User_id int) ([]int, error) {
	var Listid []int
	rows, err := db.Query("SELECT id FROM folders WHERE users_id = ?", &User_id)
	if err != nil {
		return nil, err
	}

	defer rows.Close()

	for rows.Next() {
		var id int
		rows.Scan(&id)

		Listid = append(Listid, id)
	}
	return Listid, nil
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
	http.Handle("/css/", http.StripPrefix("/css/", http.FileServer(http.Dir("../frontend/src/css"))))
	http.Handle("/fonts/", http.StripPrefix("/fonts/", http.FileServer(http.Dir("../frontend/src/fonts"))))
	http.HandleFunc("/", acceuilHandle)
	http.HandleFunc("/login", loginhandle)
	http.HandleFunc("/register", registerhandle)
	http.HandleFunc("/verify", verifyhandle)
	http.HandleFunc("/cloud/acceuil/", CloudAcceuilHandle)
	http.HandleFunc("/cloud/corbeille/", CloudCorbeilleHandle)
	http.HandleFunc("/logout", Logouthandle)
	http.HandleFunc("/upload", uploadfilehandle)
	http.HandleFunc("/createfolder", folderCreationhandle)
	http.HandleFunc("/deletefile", softdeleteFileHandle)
	http.HandleFunc("/deletefolder", softdeletefolderhandle)
	http.HandleFunc("/downloadfile", downloadhandle)
	http.HandleFunc("/renamefolder", renamedossier)
	http.HandleFunc("/renamefile", renamefile)
	http.HandleFunc("/harddeleteall", HardDeleteAll)
	http.HandleFunc("/harddeletefile", harddeleteFile)
	http.HandleFunc("/harddeleteFolder", harddeleteFolder)
	http.HandleFunc("/restorfile", restorfile)
	http.HandleFunc("/restorfolder", restorfolder)
	http.HandleFunc("/uploadfolder", uploadFolder)
	http.HandleFunc("/downloadfolder", downloadFolder)

	http.HandleFunc("/cloud/replace/", moveHandle)

	log.Println("Serveur sur http://localhost:8080")
	http.ListenAndServe(":8080", nil)

}
