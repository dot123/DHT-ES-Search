# DHT 搜索引擎

> 本项目基于 mr-mmajoR/Bittorrent-DHT-Tracker 进行改造，主要针对搜索功能进行了优化，旨在实现海量数据的毫秒级响应。我们将原有的 MySQL 全文搜索替换为 Elasticsearch，以提升搜索效率，并对分页实现进行了优化，显著提升了系统的响应速度和用户体验。

---

## 项目概述
这是一个基于 DHT 网络的 BT 种子搜索引擎，使用 Go 语言开发的分布式爬虫和搜索系统。项目采用双数据库架构，结合 MySQL 和 Elasticsearch 提供高效的数据存储和搜索服务。

---

## 核心技术栈

### 搜索引擎迁移
- **Elasticsearch 客户端**
  - go-elasticsearch/v8
  - 自定义 mapping 配置
  - search_after 深分页

### 数据同步
- **Logstash**
  - JDBC 输入插件
  - 数据类型转换
  - 增量同步配置

### 分词系统
- **IK 分词器**
  - 中文分词支持
  - 自定义词典
  - 智能分词模式

### 搜索优化
- **精确匹配**
  - operator: "and"
  - minimum_should_match: "100%"
  - analyzer: "standard"

---

## Web 接口实现

### Elasticsearch 搜索实现
```go
// 搜索查询构建
searchQuery := map[string]interface{}{
    "query": map[string]interface{}{
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
    "sort": []map[string]interface{}{
        {"updated": map[string]interface{}{"order": "desc"}},
        {"id": map[string]interface{}{"order": "asc"}},
    },
    "size": pageSize,
}
```

### 分页处理
- 使用 search_after 实现深分页
- 维护前后页的 sort 值
- 支持双向导航
```go
// 分页参数处理
if searchAfter != nil {
    searchQuery["search_after"] = searchAfter
}

// 获取下一页的 sort 值
sortValues := hit.(map[string]interface{})["sort"].([]interface{})
lastSort = sortValues
```

### 排序实现
```go
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
```

---

## 数据迁移与配置

### Elasticsearch 映射配置
**mapping.json**
```json
{
    "mappings": {
        "properties": {
            "infohash": {
                "type": "keyword"
            },
            "name": {
                "type": "keyword"
            },
            "length": {
                "type": "long"
            },
            "files": {
                "type": "boolean"
            },
            "addeded": {
                "type": "date"
            },
            "updated": {
                "type": "date"
            },
            "cnt": {
                "type": "integer"
            },
            "textindex": {
                "type": "text",
                "analyzer": "ik_max_word",
                "search_analyzer": "ik_smart"
            }
        }
    }
}
```

### IK 分词配置
1. **安装 IK 分词器插件**
   ```bash
   bin/elasticsearch-plugin install https://get.infini.cloud/elasticsearch/analysis-ik/8.16.1
   ```
   重启 Elasticsearch 服务：
   ```bash
   systemctl restart elasticsearch
   ```

2. **配置 IK 自定义词库**
   编辑 `config/analysis-ik/` 下的 `custom.txt` 文件，添加需要的自定义分词。

3. **验证 IK 分词器安装**
   ```bash
   curl -X POST "http://localhost:9200/_analyze" -H 'Content-Type: application/json' -d'{
       "analyzer": "ik_max_word",
       "text": "测试分词效果"
   }'
   ```

### Logstash 配置
**mysql_to_es.conf**
```plaintext
input {
  jdbc {
    jdbc_driver_library => "D:/logstash-8.16.1/logstash-core/lib/jars/mysql-connector-java-5.1.49.jar"
    jdbc_driver_class => "com.mysql.jdbc.Driver"
    jdbc_connection_string => "jdbc:mysql://localhost:3306/dhtbt"
    jdbc_user => "root"
    jdbc_password => "your_password"
    # 查询要导入的数据
    statement => "SELECT id, infohash, name, length, files, addeded, updated, cnt, textindex FROM infohash"
    jdbc_paging_enabled => "true"
    jdbc_page_size => "1000"
  }
}

filter {
  mutate {
    convert => { "length" => "integer" }
    convert => { "files" => "boolean" }
  }
}

output {
  elasticsearch {
    hosts => ["http://localhost:9200"]
    index => "infohash_index"
    document_id => "%{id}" # 使用 MySQL 的 id 字段作为 Elasticsearch 的文档 ID
    action => "index"
  }
}
```

### 数据迁移步骤
1. **创建 Elasticsearch 索引**
   ```bash
   curl -X PUT "http://localhost:9200/infohash_index" \
   -H "Content-Type: application/json" \
   -d @mapping.json
   ```
2. **配置 Logstash 并启动服务**
   ```bash
   bin/logstash -f mysql_to_es.conf
   ```
3. **验证数据同步**
   ```bash
   # 检查索引文档数量
   curl -X GET "http://localhost:9200/infohash_index/_count"

   # 检查映射配置是否正确
   curl -X GET "http://localhost:9200/infohash_index/_mapping"
   ```

---

## 编译与启动

### 环境要求
- Go 1.16+
- MySQL 5.7+
- Elasticsearch 8.x
- Logstash 8.x

### 配置文件
**config.json**
```json
{
    "database": {
        "host": "(localhost:3306)",
        "name": "dhtbt",
        "user": "root",
        "password": "your_password"
    },
    "elasticsearch": {
        "url": "http://localhost:9200"
    },
    "spider": {
        "port": "6882"
    },
    "webinterface": {
        "interface": "",
        "port": "8080"
    }
}
```

### 编译程序
```bash
# 编译爬虫程序
go build -o spider spider.go

# 编译 Web 接口程序
go build -o webinterface webinterface.go
```

### 启动服务
1. 启动爬虫：
   ```bash
   ./spider
   ```
2. 启动 Web 接口：
   ```bash
   ./webinterface
   ```

---

## 注意事项与问题处理

### 常见问题
1. **数据库连接错误**
   - 检查 MySQL 服务是否运行
   - 验证用户名密码是否正确
   - 确认数据库名称是否存在

2. **Elasticsearch 连接问题**
   - 检查 ES 服务状态
   - 验证 ES 版本兼容性
   - 确认 mapping 配置正确

3. **端口占用问题**
   ```bash
   lsof -i :6882
   lsof -i :8080
   kill -9 <PID>
   ```

4. **权限问题**
   ```bash
   chmod +x spider webinterface
   ```
