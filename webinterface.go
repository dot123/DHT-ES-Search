package main

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"github.com/elastic/go-elasticsearch/v8"
	_ "github.com/go-sql-driver/mysql"
	"github.com/gorilla/mux"
	"github.com/olebedev/config"
	"html/template"
	"io"
	"log"
	"net/http"
	"os"
	"sort"
	"strconv"
	"strings"
)

// 数据结构定义
type (
	file struct {
		Path   string
		Length string
	}

	Files []file

	bitTorrent struct {
		Id        int64
		InfoHash  string
		Name      string
		HaveFiles bool
		Files     []file
		Length    string
	}

	MainData struct {
		Title           string
		CountOfTorrents int
		Lastest         []bitTorrent
		Populatest      []bitTorrent
	}

	SearchData struct {
		Title      string       // 页面标题
		Query      string       // 搜索关键词
		Order      string       // 排序方式
		Founded    []bitTorrent // 搜索结果
		Page       int          // 当前页码
		TotalPages int          // 总页数
		TotalCount int          // 搜索结果数
		PrevSort   string       // 上一页的 sort 值
		NextSort   string       // 下一页的 sort 值
	}

	DetailData struct {
		Title   string
		Torrent bitTorrent
		Addeded string
		Updated string
	}
)

// 应用配置结构
type AppConfig struct {
	DB            *sql.DB
	ES            *elasticsearch.Client
	Logger        *log.Logger
	Config        *config.Config
	BindPort      string
	BindInterface string
	Templates     *Templates
}

type Templates struct {
	Main    *template.Template
	Search  *template.Template
	Details *template.Template
}

const (
	logFileName    = "webinterface.log"
	configFileName = "config.json"
)

// Files 排序接口实现
func (slice Files) Len() int           { return len(slice) }
func (slice Files) Less(i, j int) bool { return slice[i].Path < slice[j].Path }
func (slice Files) Swap(i, j int)      { slice[i], slice[j] = slice[j], slice[i] }

// 应用初始化
func newAppConfig() (*AppConfig, error) {
	app := &AppConfig{}

	if err := app.setupLogger(); err != nil {
		return nil, fmt.Errorf("logger setup failed: %v", err)
	}

	if err := app.loadConfig(); err != nil {
		return nil, fmt.Errorf("config loading failed: %v", err)
	}

	if err := app.setupDatabase(); err != nil {
		return nil, fmt.Errorf("database setup failed: %v", err)
	}

	if err := app.setupElasticsearch(); err != nil {
		return nil, fmt.Errorf("elasticsearch setup failed: %v", err)
	}

	if err := app.setupTemplates(); err != nil {
		return nil, fmt.Errorf("template setup failed: %v", err)
	}

	return app, nil
}

func (app *AppConfig) setupLogger() error {
	f, err := os.OpenFile(logFileName, os.O_RDWR|os.O_CREATE|os.O_APPEND, 0666)
	if err != nil {
		return err
	}

	multi := io.MultiWriter(f, os.Stdout)
	app.Logger = log.New(multi, "main: ", log.Ldate|log.Ltime|log.Lshortfile)
	return nil
}

func (app *AppConfig) loadConfig() error {
	cfg, err := config.ParseJsonFile(configFileName)
	if err != nil {
		return err
	}
	app.Config = cfg

	app.BindPort, _ = cfg.String("webinterface.port")
	app.BindInterface, _ = cfg.String("webinterface.interface")

	return nil
}

func (app *AppConfig) setupDatabase() error {
	host, _ := app.Config.String("database.host")
	name, _ := app.Config.String("database.name")
	user, _ := app.Config.String("database.user")
	pass, _ := app.Config.String("database.password")

	dsn := fmt.Sprintf("%s:%s@%s/%s", user, pass, host, name)
	db, err := sql.Open("mysql", dsn)
	if err != nil {
		return err
	}

	if err := db.Ping(); err != nil {
		return err
	}

	app.DB = db
	return nil
}

// 创建一个FuncMap，包含 sub、add、max、min 和 div 函数
var funcMap = template.FuncMap{
	"sub": func(a, b int) int { return a - b },
	"add": func(a, b int) int { return a + b },
	"max": func(a, b int) int {
		if a > b {
			return a
		}
		return b
	},
	"min": func(a, b int) int {
		if a < b {
			return a
		}
		return b
	},
	"div": func(a, b int) int { return a / b },
}

func (app *AppConfig) setupTemplates() error {
	var err error
	app.Templates = &Templates{}

	app.Templates.Main, err = template.New("main").ParseFiles("templates/base.html", "templates/main.html")
	if err != nil {
		return err
	}

	app.Templates.Search, err = template.New("search").Funcs(funcMap).ParseFiles("templates/base.html", "templates/search.html")
	if err != nil {
		return err
	}

	app.Templates.Details, err = template.New("details").ParseFiles("templates/base.html", "templates/details.html")
	if err != nil {
		return err
	}

	return nil
}

func humanizeFileSize(length int) string {
	if length >= 1024*1024*1024 {
		return fmt.Sprintf("%d", (1.0*length)/(1024*1024*1024)) + " Gb"
	}
	if length >= 1024*1024 {
		return fmt.Sprintf("%d", (1.0*length)/(1024*1024)) + " Mb"
	}
	if length >= 1024 {
		return fmt.Sprintf("%d", length/1024) + "Kb"
	}
	return fmt.Sprintf("%d", length)
}

func (app *AppConfig) getListOfTorrents(sqlQuery string) []bitTorrent {
	var (
		id       int64
		infohash string
		name     string
		length   int
		files    bool
	)

	var res []bitTorrent
	rows, err := app.DB.Query(sqlQuery)
	if err != nil {
		app.Logger.Printf("Query error: %v", err)
		return res
	}
	defer rows.Close()

	for rows.Next() {
		if err := rows.Scan(&id, &infohash, &name, &length, &files); err != nil {
			app.Logger.Printf("Row scan error: %v", err)
			continue
		}

		res = append(res, bitTorrent{
			Id:        id,
			InfoHash:  infohash,
			Name:      name,
			Length:    humanizeFileSize(length),
			HaveFiles: files,
		})
	}

	return res
}

func (app *AppConfig) mainHandler(w http.ResponseWriter, r *http.Request) {
	// 获取总数量
	countQuery := map[string]interface{}{
		"track_total_hits": true,
		"size":             0, // 只获取总数，不需要具体文档
	}

	var buf bytes.Buffer
	if err := json.NewEncoder(&buf).Encode(countQuery); err != nil {
		app.Logger.Printf("Error encoding count query: %v", err)
		http.Error(w, "Internal Server Error", http.StatusInternalServerError)
		return
	}

	// 执行计数查询
	res, err := app.ES.Search(
		app.ES.Search.WithContext(context.Background()),
		app.ES.Search.WithIndex("infohash_index"),
		app.ES.Search.WithBody(&buf),
	)
	if err != nil {
		app.Logger.Printf("Error getting count: %v", err)
		http.Error(w, "Internal Server Error", http.StatusInternalServerError)
		return
	}
	defer res.Body.Close()

	var countResult map[string]interface{}
	if err := json.NewDecoder(res.Body).Decode(&countResult); err != nil {
		app.Logger.Printf("Error parsing count response: %v", err)
		http.Error(w, "Internal Server Error", http.StatusInternalServerError)
		return
	}

	countOfTorrents := int(countResult["hits"].(map[string]interface{})["total"].(map[string]interface{})["value"].(float64))

	// 获取最新种子
	latestQuery := map[string]interface{}{
		"sort": []map[string]interface{}{
			{"updated": map[string]interface{}{"order": "desc"}},
			{"id": map[string]interface{}{"order": "asc"}},
		},
		"size": 50,
	}

	buf.Reset()
	if err := json.NewEncoder(&buf).Encode(latestQuery); err != nil {
		app.Logger.Printf("Error encoding latest query: %v", err)
		http.Error(w, "Internal Server Error", http.StatusInternalServerError)
		return
	}

	// 获取最新种子
	latestRes, err := app.ES.Search(
		app.ES.Search.WithContext(context.Background()),
		app.ES.Search.WithIndex("infohash_index"),
		app.ES.Search.WithBody(&buf),
	)
	if err != nil {
		app.Logger.Printf("Error getting latest torrents: %v", err)
		http.Error(w, "Internal Server Error", http.StatusInternalServerError)
		return
	}
	defer latestRes.Body.Close()

	// 获取最受欢迎的种子
	popularQuery := map[string]interface{}{
		"sort": []map[string]interface{}{
			{"cnt": map[string]interface{}{"order": "desc"}},
			{"id": map[string]interface{}{"order": "asc"}},
		},
		"size": 50,
	}

	buf.Reset()
	if err := json.NewEncoder(&buf).Encode(popularQuery); err != nil {
		app.Logger.Printf("Error encoding popular query: %v", err)
		http.Error(w, "Internal Server Error", http.StatusInternalServerError)
		return
	}

	popularRes, err := app.ES.Search(
		app.ES.Search.WithContext(context.Background()),
		app.ES.Search.WithIndex("infohash_index"),
		app.ES.Search.WithBody(&buf),
	)
	if err != nil {
		app.Logger.Printf("Error getting popular torrents: %v", err)
		http.Error(w, "Internal Server Error", http.StatusInternalServerError)
		return
	}
	defer popularRes.Body.Close()

	// 解析结果
	var latestResult, popularResult map[string]interface{}
	if err := json.NewDecoder(latestRes.Body).Decode(&latestResult); err != nil {
		app.Logger.Printf("Error parsing latest response: %v", err)
		http.Error(w, "Internal Server Error", http.StatusInternalServerError)
		return
	}
	if err := json.NewDecoder(popularRes.Body).Decode(&popularResult); err != nil {
		app.Logger.Printf("Error parsing popular response: %v", err)
		http.Error(w, "Internal Server Error", http.StatusInternalServerError)
		return
	}

	// 转换结果为 bitTorrent 结构
	lastest := make([]bitTorrent, 0)
	for _, hit := range latestResult["hits"].(map[string]interface{})["hits"].([]interface{}) {
		source := hit.(map[string]interface{})["_source"].(map[string]interface{})

		// 添加空值检查
		var id int64
		var length int

		if idVal, ok := source["id"].(float64); ok {
			id = int64(idVal)
		}

		if lengthVal, ok := source["length"].(float64); ok {
			length = int(lengthVal)
		}

		// 检查必要字段是否存在
		infohash, _ := source["infohash"].(string)
		name, _ := source["name"].(string)
		files, _ := source["files"].(bool)

		lastest = append(lastest, bitTorrent{
			Id:        id,
			InfoHash:  infohash,
			Name:      name,
			Length:    humanizeFileSize(length),
			HaveFiles: files,
		})
	}

	populatest := make([]bitTorrent, 0)
	for _, hit := range popularResult["hits"].(map[string]interface{})["hits"].([]interface{}) {
		source := hit.(map[string]interface{})["_source"].(map[string]interface{})

		// 添加空值检查
		var id int64
		var length int

		if idVal, ok := source["id"].(float64); ok {
			id = int64(idVal)
		}

		if lengthVal, ok := source["length"].(float64); ok {
			length = int(lengthVal)
		}

		// 检查必要字段是否存在
		infohash, _ := source["infohash"].(string)
		name, _ := source["name"].(string)
		files, _ := source["files"].(bool)

		populatest = append(populatest, bitTorrent{
			Id:        id,
			InfoHash:  infohash,
			Name:      name,
			Length:    humanizeFileSize(length),
			HaveFiles: files,
		})
	}

	data := MainData{
		Title:           "Welcome to DHT search engine!",
		CountOfTorrents: countOfTorrents,
		Lastest:         lastest,
		Populatest:      populatest,
	}

	app.Logger.Printf("Main page opened. Count of torrents: %d", countOfTorrents)

	if err := app.Templates.Main.ExecuteTemplate(w, "base", data); err != nil {
		app.Logger.Printf("Template execution error: %v", err)
		http.Error(w, "Template Error", http.StatusInternalServerError)
	}
}

// ES 连接
func (app *AppConfig) setupElasticsearch() error {
	esURL, _ := app.Config.String("elasticsearch.url")
	if esURL == "" {
		esURL = "http://localhost:9200"
	}

	cfg := elasticsearch.Config{
		Addresses: []string{esURL},
	}

	client, err := elasticsearch.NewClient(cfg)
	if err != nil {
		return fmt.Errorf("elasticsearch connection error: %v", err)
	}

	app.ES = client
	return nil
}

func (app *AppConfig) searchTorrents(query string, order string, page, pageSize int, searchAfter []interface{}) ([]bitTorrent, int, []interface{}, error) {
	var buf bytes.Buffer

	// 构建搜索查询
	searchQuery := map[string]interface{}{
		"query": map[string]interface{}{
			"bool": map[string]interface{}{
				"must": []map[string]interface{}{},
			},
		},
		"sort": []map[string]interface{}{},
		"size": pageSize,
	}

	// 添加查询条件
	if query != "" {
		searchQuery["query"].(map[string]interface{})["bool"].(map[string]interface{})["must"] = []map[string]interface{}{
			{
				"match": map[string]interface{}{
					"textindex": map[string]interface{}{
						"query":                query,
						"operator":             "and",
						"minimum_should_match": "100%",
						"analyzer":             "standard",
						"zero_terms_query":     "none",
					},
				},
			},
		}
	}

	// 根据排序方式添加排序字段
	if order == "cnt" {
		searchQuery["sort"] = []map[string]interface{}{
			{"cnt": map[string]interface{}{"order": "desc"}},
			{"id": map[string]interface{}{"order": "asc"}},
		}
	} else {
		searchQuery["sort"] = []map[string]interface{}{
			{"updated": map[string]interface{}{"order": "desc"}},
			{"id": map[string]interface{}{"order": "asc"}},
		}
	}

	// 如果有 search_after 数，加入到查询中
	if searchAfter != nil {
		searchQuery["search_after"] = searchAfter
	}

	// 编码
	if err := json.NewEncoder(&buf).Encode(searchQuery); err != nil {
		return nil, 0, nil, fmt.Errorf("error encoding search query: %s", err)
	}

	// 送请求
	res, err := app.ES.Search(
		app.ES.Search.WithContext(context.Background()),
		app.ES.Search.WithIndex("infohash_index"),
		app.ES.Search.WithBody(&buf),
		app.ES.Search.WithTrackTotalHits(true),
	)
	if err != nil {
		return nil, 0, nil, fmt.Errorf("error executing search: %s", err)
	}
	defer res.Body.Close()

	// 解析响应
	var result map[string]interface{}
	if err := json.NewDecoder(res.Body).Decode(&result); err != nil {
		return nil, 0, nil, fmt.Errorf("error parsing search response: %s", err)
	}

	// 获取总命中数
	totalHits := int(result["hits"].(map[string]interface{})["total"].(map[string]interface{})["value"].(float64))

	// 提取搜索结果
	var torrents []bitTorrent
	var lastSort []interface{}
	hits := result["hits"].(map[string]interface{})["hits"].([]interface{})

	for _, hit := range hits {
		source := hit.(map[string]interface{})["_source"].(map[string]interface{})
		sortValues := hit.(map[string]interface{})["sort"].([]interface{})

		id := int64(source["id"].(float64))
		length := int(source["length"].(float64))

		torrents = append(torrents, bitTorrent{
			Id:        id,
			InfoHash:  source["infohash"].(string),
			Name:      source["name"].(string),
			Length:    humanizeFileSize(length),
			HaveFiles: source["files"].(bool),
		})
		lastSort = sortValues
	}

	return torrents, totalHits, lastSort, nil
}

func (app *AppConfig) searchHandler(w http.ResponseWriter, r *http.Request) {
	defer func() {
		if err := recover(); err != nil {
			app.Logger.Printf("Panic in searchHandler: %v", err)
			http.Error(w, "Internal Server Error", http.StatusInternalServerError)
		}
	}()

	query := r.FormValue("q")
	order := r.FormValue("order")
	if order != "cnt" {
		order = "updated"
	}

	pageStr := r.FormValue("page")
	if pageStr == "" {
		pageStr = "1"
	}

	pageNum, err := strconv.Atoi(pageStr)
	if err != nil || pageNum < 1 {
		pageNum = 1
	}

	pageSize := 30

	// 获取总记录数和计算总页数
	_, totalCount, _, err := app.searchTorrents(query, order, 1, 0, nil)
	if err != nil {
		app.Logger.Printf("Error getting total count: %v", err)
		http.Error(w, "Search Error", http.StatusInternalServerError)
		return
	}

	// 计算总页数
	totalPages := (totalCount + pageSize - 1) / pageSize
	if totalPages == 0 {
		totalPages = 1
	}

	// 验证并修正页码
	if pageNum > totalPages {
		pageNum = totalPages
	}
	if pageNum < 1 {
		pageNum = 1
	}

	// 获取当前页数据
	var currentPageResults []bitTorrent
	var currentSort []interface{}
	var prevSort, nextSort string

	// 从请求中获取 sort 值
	if sortStr := r.FormValue("sort"); sortStr != "" {
		if err = json.Unmarshal([]byte(sortStr), &currentSort); err != nil {
			app.Logger.Printf("Error parsing sort value: %v", err)
			http.Error(w, "Invalid sort value", http.StatusBadRequest)
			return
		}
	}

	// 从请求中获取 prevSort 值
	if prevSortStr := r.FormValue("prevSort"); prevSortStr != "" {
		prevSort = prevSortStr
	}

	// 取当前页数据
	if pageNum == 1 {
		// 第一页直接获取
		results, _, lastSort, err := app.searchTorrents(query, order, 1, pageSize, nil)
		if err != nil {
			app.Logger.Printf("Error getting first page: %v", err)
			http.Error(w, "Search Error", http.StatusInternalServerError)
			return
		}
		currentPageResults = results
		// 保存下一页的 sort 值
		if lastSort != nil {
			if sortBytes, err := json.Marshal(lastSort); err == nil {
				nextSort = string(sortBytes)
			}
		}
	} else {
		// 用传入的 sort 值获取当前页
		results, _, lastSort, err := app.searchTorrents(query, order, 1, pageSize, currentSort)
		if err != nil {
			app.Logger.Printf("Error getting page %d: %v", pageNum, err)
			http.Error(w, "Search Error", http.StatusInternalServerError)
			return
		}
		currentPageResults = results

		// 保存下一页的 sort 值
		if lastSort != nil {
			if sortBytes, err := json.Marshal(lastSort); err == nil {
				nextSort = string(sortBytes)
			}
		}
	}

	data := SearchData{
		Title:      "Search Results: " + query,
		Query:      query,
		Order:      order,
		Founded:    currentPageResults,
		Page:       pageNum,
		TotalPages: totalPages,
		TotalCount: totalCount,
		PrevSort:   prevSort,
		NextSort:   nextSort,
	}

	app.Logger.Printf("Query: %s, Order: %s, Page: %d, TotalPages: %d", query, order, pageNum, totalPages)

	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	if err := app.Templates.Search.ExecuteTemplate(w, "base", data); err != nil {
		app.Logger.Printf("Template execution error: %v", err)
		if !strings.Contains(err.Error(), "write: broken pipe") {
			http.Error(w, "Template Error", http.StatusInternalServerError)
		}
	}
}

func (app *AppConfig) detailsHandler(w http.ResponseWriter, r *http.Request) {
	hashID := r.FormValue("id")

	// 构建查询
	query := map[string]interface{}{
		"query": map[string]interface{}{
			"term": map[string]interface{}{
				"id": hashID,
			},
		},
	}

	var buf bytes.Buffer
	if err := json.NewEncoder(&buf).Encode(query); err != nil {
		app.Logger.Printf("Error encoding query: %v", err)
		http.Error(w, "Internal Server Error", http.StatusInternalServerError)
		return
	}

	// 执行查询
	res, err := app.ES.Search(
		app.ES.Search.WithContext(context.Background()),
		app.ES.Search.WithIndex("infohash_index"),
		app.ES.Search.WithBody(&buf),
	)
	if err != nil {
		app.Logger.Printf("Error getting torrent details: %v", err)
		http.Error(w, "Internal Server Error", http.StatusInternalServerError)
		return
	}
	defer res.Body.Close()

	var result map[string]interface{}
	if err := json.NewDecoder(res.Body).Decode(&result); err != nil {
		app.Logger.Printf("Error parsing response: %v", err)
		http.Error(w, "Internal Server Error", http.StatusInternalServerError)
		return
	}

	hits := result["hits"].(map[string]interface{})["hits"].([]interface{})
	if len(hits) == 0 {
		http.Error(w, "Not Found", http.StatusNotFound)
		return
	}

	source := hits[0].(map[string]interface{})["_source"].(map[string]interface{})

	// 提取字段
	var (
		id        int64
		infohash  string
		name      string
		length    int
		haveFiles bool
		addeded   string
		updated   string
	)

	// 添加类型检查
	if idVal, ok := source["id"].(float64); ok {
		id = int64(idVal)
	}
	if lengthVal, ok := source["length"].(float64); ok {
		length = int(lengthVal)
	}
	infohash, _ = source["infohash"].(string)
	name, _ = source["name"].(string)
	haveFiles, _ = source["files"].(bool)
	addeded, _ = source["addeded"].(string)
	updated, _ = source["updated"].(string)

	app.Logger.Printf("Details request for: %s (%s)", name, humanizeFileSize(length))

	files := Files{}
	if haveFiles {
		files, err = app.getTorrentFiles(id)
		if err != nil {
			app.Logger.Printf("Error getting torrent files: %v", err)
		}
	}

	sort.Sort(files)

	data := DetailData{
		Title: "Details: " + name,
		Torrent: bitTorrent{
			InfoHash:  infohash,
			Name:      name,
			HaveFiles: haveFiles,
			Files:     files,
			Length:    humanizeFileSize(length),
		},
		Addeded: addeded,
		Updated: updated,
	}

	if err := app.Templates.Details.ExecuteTemplate(w, "base", data); err != nil {
		app.Logger.Printf("Template execution error: %v", err)
		http.Error(w, "Template Error", http.StatusInternalServerError)
	}
}

func (app *AppConfig) getTorrentFiles(torrentID int64) (Files, error) {
	files := Files{}

	rows, err := app.DB.Query("SELECT path, length FROM files WHERE infohash_id=?", torrentID)
	if err != nil {
		return files, err
	}
	defer rows.Close()

	for rows.Next() {
		var path string
		var length int

		if err := rows.Scan(&path, &length); err != nil {
			app.Logger.Printf("Error scanning file row: %v", err)
			continue
		}

		files = append(files, file{
			Path:   path,
			Length: humanizeFileSize(length),
		})
	}

	return files, nil
}

func (app *AppConfig) setupRoutes() *mux.Router {
	r := mux.NewRouter()

	r.HandleFunc("/", app.mainHandler).Methods("GET")
	r.HandleFunc("/search/", app.searchHandler).Methods("GET")
	r.HandleFunc("/details/", app.detailsHandler).Methods("GET")

	staticHandler := http.StripPrefix("/static/", http.FileServer(http.Dir("./static/")))
	r.PathPrefix("/static/").Handler(staticHandler)

	return r
}

func main() {
	app, err := newAppConfig()
	if err != nil {
		log.Fatalf("Application initialization failed: %v", err)
	}
	defer app.DB.Close()

	router := app.setupRoutes()

	app.Logger.Printf("Listening on %s:%s", app.BindInterface, app.BindPort)
	if err := http.ListenAndServe(app.BindInterface+":"+app.BindPort, router); err != nil {
		app.Logger.Fatalf("Server failed: %v", err)
	}
}
