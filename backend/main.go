package main

import (
	"database/sql"
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
)

// var and struct

var db *sql.DB
var err error
var tpl *template.Template

// function de base

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

func parseTemplates() (*template.Template, error) {
	tpl, err = template.ParseFiles(
		"../frontend/src/html/acceuil.html",
		"../frontend/src/html/login.html",
		"../frontend/src/html/register.html",
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

func uploadHandlerTEST(w http.ResponseWriter, r *http.Request) {
	// Récupération des infos du fichier
	file, fileheader, err := r.FormFile("file")

	if err != nil {
		log.Println(err)
	}

	defer file.Close()

	println(fileheader.Filename, fileheader.Size, fileheader.Header.Get("content-type"))

	// Création du fichier vide
	out, err := os.Create("../storage/" + fileheader.Filename)

	if err != nil {
		log.Println(err)
	}

	defer out.Close()

	// Copie du contenu dans le fichier précédement vide
	_, err = io.Copy(out, file)

	if err != nil {
		log.Println(err)
	}

	defer out.Close()

	// Redirection vers la page d'accueil
	http.Redirect(w, r, "/", http.StatusFound)
}

func uploadfilehandle(w http.ResponseWriter, r *http.Request) {
	Struuid := NewUUIDv4()
	folder_idstr := r.URL.Query().Get("folder_id")
	Users_idstr := r.URL.Query().Get("Users_id")
	Users_id, err := strconv.Atoi(Users_idstr)
	if err != nil {
		http.Error(w, "convertion Users_id", http.StatusInternalServerError)
		return
	}
	folder_id, err := strconv.Atoi(folder_idstr)
	if err != nil {
		http.Error(w, "convertion folder_id", http.StatusInternalServerError)
		return
	}

	file, fileheader, err := r.FormFile("file")
	if err != nil {
		http.Error(w, "ERREUR dans la recuperation du fichier", http.StatusInternalServerError)
		return
	}

	defer file.Close()

	// Création du fichier vide
	extension := filepath.Ext(fileheader.Filename)
	out, err := os.Create("../storage/" + Users_idstr + "/" + Struuid + extension)
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
		StoredPath:   "storage/" + Users_idstr + "/" + Struuid + extension,
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

		rows, err := db.Query("SELECT id,	users_id,	nom, parent_id,	created_at FROM folders WHERE users_id = ?", Users_id)
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

// handlefunc

func acceuilhandle(w http.ResponseWriter, r *http.Request) {
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

func loginhandle(w http.ResponseWriter, r *http.Request) {
	switch r.Method {
	case http.MethodGet:
		if err = tpl.ExecuteTemplate(w, "login.html", nil); err != nil {
			http.Error(w, "ERREUR de template", http.StatusInternalServerError)
			return
		}
	case http.MethodPost:
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
	default:
		http.Error(w, "Méthode non autorisée", http.StatusMethodNotAllowed)
		return
	}
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
	http.HandleFunc("/", acceuilhandle)
	http.HandleFunc("/login", loginhandle)
	http.HandleFunc("/register", registerhandle)
	http.HandleFunc("/upload", uploadfilehandle)

	log.Println("Serveur sur http://localhost:8080")
	http.ListenAndServe(":8080", nil)
}
