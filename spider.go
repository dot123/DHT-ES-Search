package main

import (
	"context"
	"database/sql"
	"encoding/hex"
	"errors"
	"flag"
	"fmt"
	_ "github.com/go-sql-driver/mysql"
	"github.com/olebedev/config"
	"github.com/shiyanhui/dht"
	"io"
	"log"
	"os"
	"os/signal"
	"sort"
	"strings"
	"sync"
	"syscall"
	"time"
)

var (
	db       *sql.DB
	l        *log.Logger
	port     string
	replacer *strings.Replacer
)

const (
	logFileName    = "logger.log"
	configFileName = "config.json"
	maxRetries     = 3
	retryDelay     = time.Second * 2
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

	// 获取并验证数据库配置
	host, _ := cfg.String("database.host")
	name, _ := cfg.String("database.name")
	user, _ := cfg.String("database.user")
	pass, _ := cfg.String("database.password")
	port, _ = cfg.String("spider.port")

	// 设置默认端口
	if port == "" {
		port = "6881"
	}

	if host == "" || name == "" || user == "" {
		l.Fatalln("数据库配置不完整")
	}

	// 连接数据库
	dsn := fmt.Sprintf("%s:%s@%s/%s", user, pass, host, name)
	var dbErr error
	db, dbErr = sql.Open("mysql", dsn)
	if dbErr != nil {
		l.Fatalln("数据库连接错误", dbErr.Error())
	}

	// 配置连接池
	db.SetMaxOpenConns(100)
	db.SetMaxIdleConns(10)
	db.SetConnMaxLifetime(time.Hour)

	// 验证数据库连接
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

// 重试机制
func withRetry(operation func() error) error {
	var err error
	for i := 0; i < maxRetries; i++ {
		if err = operation(); err == nil {
			return nil
		}
		time.Sleep(retryDelay)
		l.Printf("操作失败，正在重试 (%d/%d): %v", i+1, maxRetries, err)
	}
	return err
}

// 处理种子信息
func processTorrent(ctx context.Context, bt *bitTorrent) error {
	// 使用事务处理
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("开始事务失败: %v", err)
	}
	defer tx.Rollback()

	// 查询是否存在
	var id int64
	err = tx.QueryRowContext(ctx, "SELECT id FROM infohash WHERE infohash = ?", bt.InfoHash).Scan(&id)
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return fmt.Errorf("查询失败: %v", err)
	}

	if errors.Is(err, sql.ErrNoRows) {
		// 插入新记录
		textIndex := bt.Name
		totalLength := bt.Length

		// 处理文件信息
		if len(bt.Files) > 0 {
			for _, f := range bt.Files {
				totalLength += f.Length
				for _, p := range f.Path {
					textIndex += " " + p.(string)
				}
			}
		}

		// 生成搜索索引
		textIndex = GenerateSearchIndex(textIndex)

		result, err := tx.ExecContext(ctx,
			"INSERT INTO infohash (infohash, name, files, length, addeded, updated, textindex) VALUES (?, ?, ?, ?, NOW(), NOW(), ?)",
			bt.InfoHash, bt.Name, len(bt.Files) > 0, totalLength, textIndex)
		if err != nil {
			return fmt.Errorf("插入记录失败: %v", err)
		}

		id, err = result.LastInsertId()
		if err != nil {
			return fmt.Errorf("获取插入ID失败: %v", err)
		}

		// 插入文件信息
		if len(bt.Files) > 0 {
			stmt, err := tx.PrepareContext(ctx, "INSERT INTO files (infohash_id, path, length) VALUES (?, ?, ?)")
			if err != nil {
				return fmt.Errorf("准备文件插入语句失败: %v", err)
			}
			defer stmt.Close()

			for _, f := range bt.Files {
				path := ""
				for i, p := range f.Path {
					if i > 0 {
						path += "/"
					}
					path += p.(string)
				}
				if _, err := stmt.ExecContext(ctx, id, path, f.Length); err != nil {
					return fmt.Errorf("插入文件信息失败: %v", err)
				}
			}
		}

		l.Printf("新增种子: %s, 文件数: %d", bt.InfoHash, len(bt.Files))
	} else {
		// 更新已存在的记录
		if _, err := tx.ExecContext(ctx, "UPDATE infohash SET updated=NOW(), cnt=cnt+1 WHERE id=?", id); err != nil {
			return fmt.Errorf("更新记录失败: %v", err)
		}
		l.Printf("更新种子: %s", bt.InfoHash)
	}

	return tx.Commit()
}

func main() {
	// 创建上下文和取消函数
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	// 设置信号处理
	sigChan := make(chan os.Signal, 1)
	signal.Notify(sigChan, syscall.SIGINT, syscall.SIGTERM)

	// 启动DHT网络
	w := dht.NewWire(65536, 1024, 256)

	// 使用WaitGroup管理goroutine
	var wg sync.WaitGroup
	wg.Add(1)

	// 处理DHT响应
	go func() {
		defer wg.Done()
		for {
			select {
			case <-ctx.Done():
				return
			case resp := <-w.Response():
				metadata, err := dht.Decode(resp.MetadataInfo)
				if err != nil {
					l.Printf("解码元数据失败: %v", err)
					continue
				}

				info, ok := metadata.(map[string]interface{})
				if !ok {
					l.Printf("元数据类型错误")
					continue
				}

				if _, ok := info["name"]; !ok {
					continue
				}

				bt := bitTorrent{
					InfoHash: hex.EncodeToString(resp.InfoHash),
					Name:     info["name"].(string),
				}

				// 处理文件信息
				if v, ok := info["files"]; ok {
					vFiles := v.([]interface{})
					bt.Files = make([]file, len(vFiles))
					for i, item := range vFiles {
						f := item.(map[string]interface{})
						bt.Files[i] = file{
							Path:   f["path"].([]interface{}),
							Length: f["length"].(int),
						}
					}
				} else if v, ok := info["length"]; ok {
					bt.Length = v.(int)
				}

				// 使用重试机制处理种子信息
				if err := withRetry(func() error {
					return processTorrent(ctx, &bt)
				}); err != nil {
					l.Printf("处理种子失败: %v", err)
				}
			}
		}
	}()

	// 监听信号
	go func() {
		<-sigChan
		l.Println("收到关闭信号，正在优雅关闭...")
		cancel()
	}()

	// 启动DHT爬虫
	go w.Run()

	// DHT配置
	c := dht.NewCrawlConfig()
	c.Address = ":" + port
	c.PrimeNodes = append(c.PrimeNodes, "router.bitcomet.com:6881")

	c.OnAnnouncePeer = func(infoHash, ip string, port int) {
		// 收到新的peer通知
		w.Request([]byte(infoHash), ip, port)
	}

	// 启动DHT服务
	d := dht.New(c)
	go d.Run()

	l.Println("DHT爬虫已启动，使用端口:", port)

	// 等待所有goroutine完成
	wg.Wait()

	// 关闭资源
	if err := db.Close(); err != nil {
		l.Printf("关闭数据库连接失败: %v", err)
	}
	l.Println("程序已关闭")
}
