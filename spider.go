package main

import (
	"database/sql"
	"encoding/hex"
	"flag"
	"fmt"
	_ "github.com/go-sql-driver/mysql"
	"github.com/olebedev/config"
	"github.com/shiyanhui/dht"
	"io"
	"log"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"time"
)

var (
	db       *sql.DB
	l        *log.Logger
	cfg      *config.Config
	port     string
	replacer *strings.Replacer
)

const (
	logFileName    = "logger.log"
	configFileName = "config.json"
)

type file struct {
	Path   []interface{} `json:"path"`
	Length int           `json:"length"`
}

type bitTorrent struct {
	InfoHash string `json:"infohash"`
	Name     string `json:"name"`
	Files    []file `json:"files,omitempty"`
	Length   int    `json:"length,omitempty"`
}

func init() {
	// 初始化日志
	f, err := os.OpenFile(logFileName, os.O_RDWR|os.O_CREATE|os.O_APPEND, 0666)
	if err != nil {
		log.Fatalln("无法打开日志文件", logFileName, ":", err)
	}

	multi := io.MultiWriter(f, os.Stdout)
	l = log.New(multi, "main: ", log.Ldate|log.Ltime|log.Lshortfile)

	// 取配置文件
	cfg, err := config.ParseJsonFile(configFileName)
	if err != nil {
		l.Fatalln("无法打开配置文件", configFileName, ":", err)
	}

	// 获取数据库连接信息
	host, _ := cfg.String("database.host")
	name, _ := cfg.String("database.name")
	user, _ := cfg.String("database.user")
	pass, _ := cfg.String("database.password")
	port, _ = cfg.String("spider.port")

	// 设置默认端口
	if port == "" {
		port = "6882"
	}

	// 连接数据库
	dsn := fmt.Sprintf("%s:%s@%s/%s", user, pass, host, name)
	db, err := sql.Open("mysql", dsn)
	if err != nil {
		l.Fatalln("数据库连接错误", err.Error())
	}

	if err := db.Ping(); err != nil {
		l.Fatalln("数据库连接错误", err.Error())
	}

	portPtr := flag.String("port", port, "DHT端口")
	flag.Parse()
	port = *portPtr

	// 初始化字符串替换器
	replacer = strings.NewReplacer(
		"/", " ",
		"[", " ",
		"(", " ",
		"]", " ",
		")", " ",
		".", " ",
		"_", " ",
	)
}

func GenerateSearchIndex(text string) string {
	// 清洗文本
	text = replacer.Replace(text)
	uniq := map[string]int{}

	// 统计词频
	for _, s := range strings.Split(text, " ") {
		if s != "" {
			if cv, presen := uniq[s]; presen {
				uniq[s] = cv + 1
			} else {
				uniq[s] = 1
			}
		}
	}

	// 排序
	type kvt struct {
		Key   string
		Value int
	}
	kv := make([]kvt, 0, len(uniq))
	for k, v := range uniq {
		kv = append(kv, kvt{k, v})
	}

	sort.Slice(kv, func(i, j int) bool { return kv[i].Value > kv[j].Value })

	// 生成索引
	var indexText string
	for _, i := range kv {
		indexText += i.Key + " "
	}

	return strings.TrimSpace(indexText)
}

func restartProcess() {
	l.Println("正在重启程序...")

	// 获取当前可执行文件路径
	executable, err := os.Executable()
	if err != nil {
		l.Printf("获取可执行文件路径失败: %v", err)
		return
	}

	// 获取当前工作目录
	dir, err := filepath.Abs(filepath.Dir(executable))
	if err != nil {
		l.Printf("获取工作目录失败: %v", err)
		return
	}

	// 准备命令行参数
	args := []string{}
	if port != "6882" { // 如果不是默认端口，添加端口参数
		args = append(args, "-port", port)
	}

	// 准备新进程
	cmd := exec.Command(executable, args...)
	cmd.Dir = dir
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	cmd.Env = os.Environ()

	// 启动新进程
	if err := cmd.Start(); err != nil {
		l.Printf("启动新进程失败: %v", err)
		return
	}

	// 等待新进程完全启动
	time.Sleep(time.Second)

	// 优雅关闭当前进程
	l.Println("新进程已启动，正在关闭当前进程...")
	if err := db.Close(); err != nil {
		l.Printf("关闭数据库连接失败: %v", err)
	}

	// 获取当前进程并结束它
	process, err := os.FindProcess(os.Getpid())
	if err != nil {
		l.Printf("获取当前进程失败: %v", err)
		os.Exit(0)
		return
	}

	if err := process.Kill(); err != nil {
		l.Printf("结束当前进程失败: %v", err)
		os.Exit(0)
	}
}

func main() {
	// 启动DHT网络
	w := dht.NewWire(65536, 1024, 256)
	countDown := 100

	// 使用协程处理DHT响应
	go func() {
		for resp := range w.Response() {
			metadata, err := dht.Decode(resp.MetadataInfo)
			if err != nil {
				continue
			}
			info := metadata.(map[string]interface{})

			if _, ok := info["name"]; !ok {
				continue
			}

			bt := bitTorrent{
				InfoHash: hex.EncodeToString(resp.InfoHash),
				Name:     info["name"].(string),
			}

			var vFiles []interface{}
			haveFiles := false
			isNew := false

			// 处理文件信息
			if v, ok := info["files"]; ok {
				haveFiles = true
				vFiles = v.([]interface{})
				bt.Files = make([]file, len(vFiles))

				for i, item := range vFiles {
					f := item.(map[string]interface{})
					bt.Files[i] = file{
						Path:   f["path"].([]interface{}),
						Length: f["length"].(int),
					}
				}
			} else if _, ok := info["length"]; ok {
				bt.Length = info["length"].(int)
			}

			// 必须手动关闭查询的结果集
			infoHashSelect, err := db.Query("SELECT id FROM infohash WHERE infohash = ?", bt.InfoHash)
			if err != nil {
				l.Println("数据库查询错误", err)
				continue
			}

			var id int64
			if infoHashSelect.Next() {
				err := infoHashSelect.Scan(&id)
				if err != nil {
					l.Fatal("扫描结果错误", err)
				}

				l.Println(bt.InfoHash, "更新:", bt.Name)

				// 更新数据库记录
				upd, err := db.Prepare("UPDATE infohash SET updated=NOW(), cnt=cnt+1 WHERE id=?")
				if err != nil {
					l.Panicln("数据库更新准备失败", err)
				}

				_, err = upd.Exec(id)
				if err != nil {
					l.Println("更新失败:", err)
					upd.Close()
					continue
				}
				upd.Close()

			} else {
				isNew = true
				totalLength := 0
				textIndex := bt.Name

				// 获取文件长度和路径
				if _, ok := info["length"]; ok {
					totalLength = info["length"].(int)
				}

				if haveFiles {
					for _, item := range vFiles {
						f := item.(map[string]interface{})
						totalLength += f["length"].(int)
						for _, p := range f["path"].([]interface{}) {
							textIndex += " " + p.(string)
						}
					}
				}

				// 生成搜索索引
				textIndex = GenerateSearchIndex(textIndex)

				l.Println(bt.InfoHash, "加新条目:", bt.Name)

				// 插入新记录
				ins, err := db.Prepare("INSERT INTO infohash SET infohash=?, name=?, files=?, length=?, addeded=NOW(), updated=NOW(), textindex=?")
				if err != nil {
					l.Panicln("插入数据库失败", err)
				}

				res, err := ins.Exec(bt.InfoHash, bt.Name, haveFiles, totalLength, textIndex)
				if err != nil {
					l.Println("插入失败:", err)
					ins.Close()
					continue
				}

				id, err = res.LastInsertId()
				if err != nil {
					l.Println("获取新记录ID失败", err)
				}

				ins.Close()
			}

			infoHashSelect.Close() // 手动关闭查询结果集

			// 处理文件信息
			if haveFiles && isNew {
				ins, err := db.Prepare("INSERT INTO files SET infohash_id=?, path=?, length=?")
				if err != nil {
					l.Panicln("插入文件信息失败", err)
				}

				for _, item := range vFiles {
					f := item.(map[string]interface{})
					path := ""
					for _, p := range f["path"].([]interface{}) {
						if path != "" {
							path = path + "/" + p.(string)
						} else {
							path = p.(string)
						}
					}
					_, err = ins.Exec(id, path, f["length"].(int))
					if err != nil {
						l.Println("插入文件路径失败", err)
					}
				}

				ins.Close()
				l.Printf("%s  文件数：%d", bt.InfoHash, len(vFiles))
			}

			countDown--
			if countDown <= 0 {
				// 替换强制退出为重启
				restartProcess()
				return
			}
		}
	}()

	// 启动DHT爬虫
	go w.Run()

	// DHT配置
	config := dht.NewCrawlConfig()
	l.Println("使用端口:", port)

	config.Address = ":" + port
	config.PrimeNodes = append(config.PrimeNodes, "router.bitcomet.com:6881")

	config.OnAnnouncePeer = func(infoHash, ip string, port int) {
		// 收到新的peer通知
		w.Request([]byte(infoHash), ip, port)
	}

	// 启动DHT服务
	d := dht.New(config)
	d.Run()

	defer db.Close()
}
