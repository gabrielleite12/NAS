package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"os"
	"os/exec"
	"path"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"time"
	"embed"

	"github.com/jlaffaye/ftp"
)

//go:embed index.html
var staticFiles embed.FS

type Config struct {
	NasHost     string `json:"nas_host"`
	ApiPort     int    `json:"api_port"`
	FtpPort     int    `json:"ftp_port"`
	FtpUser     string `json:"ftp_user"`
	FtpPassword string `json:"ftp_password"`
	FtpMode     string `json:"ftp_mode"`
	FtpRoot     string `json:"ftp_root"`
	SmbShare    string `json:"smb_share"`
	SmbUser     string `json:"smb_user"`
	SmbPassword string `json:"smb_password"`
}

var (
	config     Config
	configFile = "config.json"
	ftpStorage *FTPProvider
)

type LogEntry struct {
	Time   time.Time `json:"time"`
	User   string    `json:"user"`
	Action string    `json:"action"`
	Item   string    `json:"item"`
}

type FileInfo struct {
	Name  string    `json:"name"`
	Size  uint64    `json:"size"`
	Type  string    `json:"type"` // "dir" ou "file"
	Time  time.Time `json:"time"`
	Owner string    `json:"owner"`
}

// --- FTP Provider ---
type FTPProvider struct {
	client *ftp.ServerConn
	mutex  sync.Mutex
}

func (f *FTPProvider) ensureLocked() error {
	if f.client != nil {
		if err := f.client.NoOp(); err == nil {
			return nil
		}
		f.client.Quit()
		f.client = nil
	}

	addr := fmt.Sprintf("%s:%d", config.NasHost, config.FtpPort)
	c, err := ftp.Dial(addr, ftp.DialWithTimeout(5*time.Second))
	if err != nil {
		return err
	}
	if err := c.Login(config.FtpUser, config.FtpPassword); err != nil {
		c.Quit()
		return err
	}
	f.client = c
	return nil
}

func (f *FTPProvider) Connect() error {
	f.mutex.Lock()
	defer f.mutex.Unlock()
	return f.ensureLocked()
}

func (f *FTPProvider) Status() error {
	return f.Connect()
}

func (f *FTPProvider) Reconnect() error {
	f.mutex.Lock()
	defer f.mutex.Unlock()
	if f.client != nil {
		f.client.Quit()
		f.client = nil
	}
	return f.ensureLocked()
}

func (f *FTPProvider) List(p string) ([]FileInfo, error) {
	f.mutex.Lock()
	// Reconecta sob lock para listagem fresca e sem race
	if f.client != nil {
		f.client.Quit()
		f.client = nil
	}
	if err := f.ensureLocked(); err != nil {
		f.mutex.Unlock()
		return nil, err
	}
	entries, err := f.client.List(p)
	f.mutex.Unlock()
	if err != nil {
		return nil, err
	}

	vp := p
	if strings.HasPrefix(p, config.FtpRoot) {
		vp = strings.TrimPrefix(p, config.FtpRoot)
		if vp == "" {
			vp = "/"
		}
	}
	vp = normalizeVirt(vp)

	existing := make(map[string]bool)
	var res []FileInfo
	for _, e := range entries {
		if e.Name == "." || e.Name == ".." || isHiddenMeta(e.Name) {
			continue
		}
		existing[e.Name] = true
		t := "file"
		if e.Type == ftp.EntryTypeFolder {
			t = "dir"
		}
		key := normalizeVirt(path.Join(vp, e.Name))
		res = append(res, FileInfo{
			Name:  e.Name,
			Size:  e.Size,
			Type:  t,
			Time:  e.Time,
			Owner: getOwner(key),
		})
	}
	pruneOwnersInDir(vp, existing)
	return res, nil
}

func (f *FTPProvider) Download(p string, w io.Writer) error {
	f.mutex.Lock()
	defer f.mutex.Unlock()
	if err := f.ensureLocked(); err != nil {
		return err
	}
	res, err := f.client.Retr(p)
	if err != nil {
		return err
	}
	defer res.Close()
	_, err = io.Copy(w, res)
	return err
}

func (f *FTPProvider) Upload(p string, r io.Reader) error {
	f.mutex.Lock()
	defer f.mutex.Unlock()
	if err := f.ensureLocked(); err != nil {
		return err
	}
	return f.client.Stor(p, r)
}

func (f *FTPProvider) MakeDir(p string) error {
	f.mutex.Lock()
	defer f.mutex.Unlock()
	if err := f.ensureLocked(); err != nil {
		return err
	}
	return f.client.MakeDir(p)
}

func (f *FTPProvider) Rename(oldPath, newPath string) error {
	f.mutex.Lock()
	defer f.mutex.Unlock()
	if err := f.ensureLocked(); err != nil {
		return err
	}
	return f.client.Rename(oldPath, newPath)
}

func (f *FTPProvider) Delete(p string, isDir bool) error {
	f.mutex.Lock()
	defer f.mutex.Unlock()
	if err := f.ensureLocked(); err != nil {
		return err
	}
	if isDir {
		return f.client.RemoveDirRecur(p)
	}
	return f.client.Delete(p)
}

func createDefaultConfig() {
	config = Config{
		NasHost:     "192.168.0.250",
		ApiPort:     8765,
		FtpPort:     21,
		FtpUser:     "admin",
		FtpPassword: "",
		FtpMode:     "passive",
		FtpRoot:     "/usb1_1_2",
	}
	saveConfig()
}

func loadConfig() {
	file, err := os.ReadFile(configFile)
	if err != nil {
		fmt.Println("Criando config.json padrão...")
		createDefaultConfig()
	} else {
		err = json.Unmarshal(file, &config)
		if err != nil {
			log.Fatalf("Erro ao ler config.json: %v", err)
		}
	}

	ftpStorage = &FTPProvider{}
}

func saveConfig() {
	data, _ := json.MarshalIndent(config, "", "  ")
	os.WriteFile(configFile, data, 0644)
}

// safePath valida e limpa o caminho virtual da API
func safePath(p string) string {
	p = path.Clean(p)
	if strings.Contains(p, "..") {
		return "/"
	}
	if !strings.HasPrefix(p, "/") {
		p = "/" + p
	}
	return p
}

// realPath mapeia o caminho virtual (ex: /) para o caminho real no FTP (ex: /usb1_1_2)
func realPath(p string) string {
	sp := safePath(p)
	if sp == "/" {
		return config.FtpRoot
	}
	return path.Join(config.FtpRoot, sp)
}

func getUser(r *http.Request) string {
	return r.Header.Get("X-User")
}

func corsMiddleware(next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		origin := r.Header.Get("Origin")
		w.Header().Set("Access-Control-Allow-Origin", origin)
		w.Header().Set("Access-Control-Allow-Methods", "GET, POST, DELETE, OPTIONS")
		w.Header().Set("Access-Control-Allow-Headers", "Content-Type, X-User, X-Pass")
		w.Header().Set("Access-Control-Allow-Credentials", "true")

		if r.Method == "OPTIONS" {
			w.WriteHeader(http.StatusOK)
			return
		}
		next(w, r)
	}
}

func authMiddleware(next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		user := r.Header.Get("X-User")
		pass := r.Header.Get("X-Pass")

		isLocal := strings.HasPrefix(r.RemoteAddr, "127.0.0.1:") || strings.HasPrefix(r.RemoteAddr, "[::1]:")

		if user != "leite" && user != "celio" {
			jsonError(w, http.StatusUnauthorized, "Usuário inválido")
			return
		}

		if !isLocal && pass != "091846" {
			jsonError(w, http.StatusUnauthorized, "Senha incorreta")
			return
		}

		next(w, r)
	}
}

func jsonResponse(w http.ResponseWriter, status int, data interface{}) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	json.NewEncoder(w).Encode(data)
}

func jsonError(w http.ResponseWriter, status int, message string) {
	jsonResponse(w, status, map[string]string{"error": message})
}

func handleLogin(w http.ResponseWriter, r *http.Request) {
	jsonResponse(w, http.StatusOK, map[string]string{"message": "ok"})
}

func handleStatus(w http.ResponseWriter, r *http.Request) {
	err := ftpStorage.Status()
	if err != nil {
		jsonResponse(w, http.StatusOK, map[string]interface{}{
			"status": "offline",
			"error":  err.Error(),
		})
		return
	}
	jsonResponse(w, http.StatusOK, map[string]interface{}{
		"status": "online",
	})
}

func handleFiles(w http.ResponseWriter, r *http.Request) {
	vp := safePath(r.URL.Query().Get("path"))
	rp := realPath(vp)

	files, err := ftpStorage.List(rp)
	if err != nil {
		jsonError(w, http.StatusInternalServerError, "Não foi possível listar a pasta")
		return
	}
	jsonResponse(w, http.StatusOK, files)
}

func handleDownload(w http.ResponseWriter, r *http.Request) {
	vp := safePath(r.URL.Query().Get("path"))
	rp := realPath(vp)

	w.Header().Set("Content-Disposition", fmt.Sprintf("attachment; filename=\"%s\"", filepath.Base(vp)))
	w.Header().Set("Content-Type", "application/octet-stream")

	err := ftpStorage.Download(rp, w)
	if err != nil {
		log.Printf("[API] Erro no download: %v", err)
	} else {
		// handleDownload is mostly a GET link, but if requested via fetch, we can log. 
		// Actually, standard window.location.href won't send X-User, so we skip auth logs for direct downloads unless we pass token in query.
		// For now, don't log downloads to avoid complexity, or log as 'Sistema'.
	}
}

func handleUpload(w http.ResponseWriter, r *http.Request) {
	// Até 512MB em memória para multipart; arquivos maiores vão para disco temporário
	r.ParseMultipartForm(512 << 20)
	vp := safePath(r.FormValue("path"))
	user := getUser(r)

	file, handler, err := r.FormFile("file")
	if err != nil {
		jsonError(w, http.StatusBadRequest, "Arquivo não enviado")
		return
	}
	defer file.Close()

	// Usa só o nome base (evita path traversal se o browser mandar caminho relativo)
	fileName := filepath.Base(handler.Filename)
	if fileName == "." || fileName == "/" || fileName == "" {
		jsonError(w, http.StatusBadRequest, "Nome de arquivo inválido")
		return
	}

	destVp := path.Join(vp, fileName)
	destRp := realPath(destVp)

	err = ftpStorage.Upload(destRp, file)
	if err != nil {
		jsonError(w, http.StatusInternalServerError, "Falha no upload")
		return
	}

	setOwner(destVp, user, "file")
	addLog(user, "Upload", destVp)
	jsonResponse(w, http.StatusOK, map[string]string{"message": "Upload concluído"})
}

func handleFolder(w http.ResponseWriter, r *http.Request) {
	var data struct {
		Path string `json:"path"`
		Name string `json:"name"`
	}
	json.NewDecoder(r.Body).Decode(&data)

	vp := safePath(data.Path)
	name := strings.TrimSpace(data.Name)
	if name == "" || strings.Contains(name, "/") || strings.Contains(name, "\\") {
		jsonError(w, http.StatusBadRequest, "Nome de pasta inválido")
		return
	}

	newDirVp := path.Join(vp, name)
	newDirRp := realPath(newDirVp)
	user := getUser(r)

	err := ftpStorage.MakeDir(newDirRp)
	if err != nil {
		// Se a pasta já existe, trata como sucesso (comum em upload de pastas)
		entries, listErr := ftpStorage.List(path.Dir(newDirRp))
		exists := false
		if listErr == nil {
			base := path.Base(newDirRp)
			for _, e := range entries {
				if e.Name == base && e.Type == "dir" {
					exists = true
					break
				}
			}
		}
		if !exists {
			jsonError(w, http.StatusInternalServerError, "Falha ao criar pasta")
			return
		}
	}

	setOwner(newDirVp, user, "dir")
	addLog(user, "Nova Pasta", newDirVp)
	jsonResponse(w, http.StatusOK, map[string]string{"message": "Pasta criada"})
}

func handleCreateFile(w http.ResponseWriter, r *http.Request) {
	var data struct {
		Path string `json:"path"`
		Name string `json:"name"`
	}
	json.NewDecoder(r.Body).Decode(&data)

	vp := safePath(data.Path)
	newFileVp := path.Join(vp, data.Name)
	newFileRp := realPath(newFileVp)
	user := getUser(r)

	// Create an empty file via FTP upload with empty content
	err := ftpStorage.Upload(newFileRp, strings.NewReader(""))
	if err != nil {
		jsonError(w, http.StatusInternalServerError, "Falha ao criar arquivo")
		return
	}

	setOwner(newFileVp, user, "file")
	addLog(user, "Novo Arquivo", newFileVp)
	jsonResponse(w, http.StatusOK, map[string]string{"message": "Arquivo criado"})
}

func handleRename(w http.ResponseWriter, r *http.Request) {
	var data struct {
		Path    string `json:"path"`
		OldName string `json:"old_name"`
		NewName string `json:"new_name"`
	}
	json.NewDecoder(r.Body).Decode(&data)

	vp := safePath(data.Path)
	oldVp := path.Join(vp, data.OldName)
	newVp := path.Join(vp, data.NewName)

	oldRp := realPath(oldVp)
	newRp := realPath(newVp)
	user := getUser(r)

	err := ftpStorage.Rename(oldRp, newRp)
	if err != nil {
		jsonError(w, http.StatusInternalServerError, "Falha ao renomear")
		return
	}

	renameOwner(oldVp, newVp)
	addLog(user, "Renomear", fmt.Sprintf("%s -> %s", data.OldName, data.NewName))
	jsonResponse(w, http.StatusOK, map[string]string{"message": "Renomeado com sucesso"})
}

func handleDelete(w http.ResponseWriter, r *http.Request) {
	var data struct {
		Path string `json:"path"`
		Type string `json:"type"`
	}
	json.NewDecoder(r.Body).Decode(&data)

	vp := safePath(data.Path)
	rp := realPath(vp)
	user := getUser(r)

	err := ftpStorage.Delete(rp, data.Type == "dir")
	if err != nil {
		jsonError(w, http.StatusInternalServerError, "Falha ao excluir")
		return
	}

	deleteOwnerTree(vp)
	addLog(user, "Excluir", vp)
	jsonResponse(w, http.StatusOK, map[string]string{"message": "Excluído com sucesso"})
}

func handleReadText(w http.ResponseWriter, r *http.Request) {
	vp := safePath(r.URL.Query().Get("path"))
	rp := realPath(vp)

	buf := new(bytes.Buffer)
	err := ftpStorage.Download(rp, buf)
	if err != nil {
		jsonError(w, http.StatusNotFound, "Arquivo não encontrado")
		return
	}
	jsonResponse(w, http.StatusOK, map[string]string{"content": buf.String()})
}

func handleWriteText(w http.ResponseWriter, r *http.Request) {
	var data struct {
		Path    string `json:"path"`
		Content string `json:"content"`
	}
	json.NewDecoder(r.Body).Decode(&data)

	vp := safePath(data.Path)
	rp := realPath(vp)
	user := getUser(r)

	reader := strings.NewReader(data.Content)
	err := ftpStorage.Upload(rp, reader)
	if err != nil {
		jsonError(w, http.StatusInternalServerError, "Falha ao salvar o arquivo")
		return
	}

	addLog(user, "Editar Arquivo", vp)
	jsonResponse(w, http.StatusOK, map[string]string{"message": "Arquivo salvo"})
}

func handleLogs(w http.ResponseWriter, r *http.Request) {
	jsonResponse(w, http.StatusOK, getLogs())
}

func handleFormat(w http.ResponseWriter, r *http.Request) {
	user := getUser(r)
	entries, err := ftpStorage.List(config.FtpRoot)
	if err != nil {
		jsonError(w, http.StatusInternalServerError, "Erro ao listar disco")
		return
	}

	var errs []string
	for _, e := range entries {
		if e.Name == "." || e.Name == ".." || isHiddenMeta(e.Name) {
			continue
		}
		p := path.Join(config.FtpRoot, e.Name)
		if delErr := ftpStorage.Delete(p, e.Type == "dir"); delErr != nil {
			log.Printf("[FORMAT] Erro ao excluir %s: %v", p, delErr)
			errs = append(errs, e.Name)
		}
	}

	ftpStorage.Reconnect()
	clearAllMeta()

	if len(errs) > 0 {
		addLog(user, "Formatar (parcial)", fmt.Sprintf("Não foi possível excluir: %s", strings.Join(errs, ", ")))
		saveDB()
		jsonError(w, http.StatusInternalServerError, fmt.Sprintf("Falha ao excluir: %s", strings.Join(errs, ", ")))
		return
	}

	addLog(user, "Formatar", "Disco completamente esvaziado")
	saveDB()

	jsonResponse(w, http.StatusOK, map[string]string{"message": "Disco Esvaziado com sucesso"})
}

func handleDiskSpace(w http.ResponseWriter, r *http.Request) {
	cmdStr := fmt.Sprintf("//%s/%s", config.NasHost, config.SmbShare)
	authStr := fmt.Sprintf("%s%%%s", config.SmbUser, config.SmbPassword)

	cmd := exec.Command("smbclient", cmdStr, "-U", authStr, "--option=client min protocol=NT1", "-c", "dir")
	out, err := cmd.CombinedOutput()
	if err != nil {
		jsonError(w, http.StatusInternalServerError, "Erro ao obter espaço em disco: "+err.Error())
		return
	}

	re := regexp.MustCompile(`(\d+)\s+blocks\s+of\s+size\s+(\d+)\.\s+(\d+)\s+blocks\s+available`)
	matches := re.FindStringSubmatch(string(out))
	if len(matches) != 4 {
		jsonError(w, http.StatusInternalServerError, "Falha ao interpretar resposta de espaço")
		return
	}

	totalBlocks, _ := strconv.ParseUint(matches[1], 10, 64)
	blockSize, _ := strconv.ParseUint(matches[2], 10, 64)
	availBlocks, _ := strconv.ParseUint(matches[3], 10, 64)

	totalBytes := totalBlocks * blockSize
	freeBytes := availBlocks * blockSize
	usedBytes := totalBytes - freeBytes

	jsonResponse(w, http.StatusOK, map[string]interface{}{
		"total": totalBytes,
		"free":  freeBytes,
		"used":  usedBytes,
	})
}

func startServer() string {
	loadConfig()
	initDB()

	fmt.Println("NAS Local iniciado")
	fmt.Printf("API (Rede): http://0.0.0.0:%d\n", config.ApiPort)

	go func() {
		err := ftpStorage.Connect()
		if err != nil {
			fmt.Printf("Erro inicial de conexão (FTP): %v\n", err)
		} else {
			fmt.Printf("FTP: conectado\n")
			loadDB()
		}
	}()

	mux := http.NewServeMux()

	// Endpoints that require auth
	mux.HandleFunc("/api/login", corsMiddleware(authMiddleware(handleLogin)))
	mux.HandleFunc("/api/files", corsMiddleware(authMiddleware(handleFiles)))
	mux.HandleFunc("/api/upload", corsMiddleware(authMiddleware(handleUpload)))
	mux.HandleFunc("/api/folder", corsMiddleware(authMiddleware(handleFolder)))
	mux.HandleFunc("/api/create-file", corsMiddleware(authMiddleware(handleCreateFile)))
	mux.HandleFunc("/api/rename", corsMiddleware(authMiddleware(handleRename)))
	mux.HandleFunc("/api/file", corsMiddleware(authMiddleware(handleDelete)))
	mux.HandleFunc("/api/read", corsMiddleware(authMiddleware(handleReadText)))
	mux.HandleFunc("/api/write", corsMiddleware(authMiddleware(handleWriteText)))
	mux.HandleFunc("/api/logs", corsMiddleware(authMiddleware(handleLogs)))
	mux.HandleFunc("/api/format", corsMiddleware(authMiddleware(handleFormat)))
	
	mux.HandleFunc("/api/shutdown", corsMiddleware(authMiddleware(func(w http.ResponseWriter, r *http.Request) {
		jsonResponse(w, http.StatusOK, map[string]string{"message": "Servidor encerrando..."})
		go func() {
			time.Sleep(1 * time.Second)
			os.Exit(0)
		}()
	})))
	
	// No auth required
	mux.HandleFunc("/api/status", corsMiddleware(handleStatus))
	mux.HandleFunc("/api/diskspace", corsMiddleware(handleDiskSpace))
	mux.HandleFunc("/api/download", corsMiddleware(handleDownload)) // can't easily auth via standard link click

	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		content, err := staticFiles.ReadFile("index.html")
		if err != nil {
			http.Error(w, "File not found", http.StatusNotFound)
			return
		}
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		w.Write(content)
	})

	go func() {
		err := http.ListenAndServe(fmt.Sprintf("0.0.0.0:%d", config.ApiPort), mux)
		if err != nil {
			log.Fatalf("Erro no servidor web: %v", err)
		}
	}()

	return fmt.Sprintf("http://localhost:%d", config.ApiPort)
}

func main() {
	url := startServer()
	platformRun(url)
}

