-- --------------------------------------------------------
-- 主机:                           127.0.0.1
-- 服务器版本:                        5.7.44 - MySQL Community Server (GPL)
-- 服务器操作系统:                      Win64
-- HeidiSQL 版本:                  12.7.0.6856
-- --------------------------------------------------------

/*!40101 SET @OLD_CHARACTER_SET_CLIENT=@@CHARACTER_SET_CLIENT */;
/*!40101 SET NAMES utf8 */;
/*!50503 SET NAMES utf8mb4 */;
/*!40103 SET @OLD_TIME_ZONE=@@TIME_ZONE */;
/*!40103 SET TIME_ZONE='+00:00' */;
/*!40014 SET @OLD_FOREIGN_KEY_CHECKS=@@FOREIGN_KEY_CHECKS, FOREIGN_KEY_CHECKS=0 */;
/*!40101 SET @OLD_SQL_MODE=@@SQL_MODE, SQL_MODE='NO_AUTO_VALUE_ON_ZERO' */;
/*!40111 SET @OLD_SQL_NOTES=@@SQL_NOTES, SQL_NOTES=0 */;


-- 导出 dhtbt 的数据库结构
CREATE DATABASE IF NOT EXISTS `dhtbt` /*!40100 DEFAULT CHARACTER SET utf8 */;
USE `dhtbt`;

-- 导出  表 dhtbt.files 结构
CREATE TABLE IF NOT EXISTS `files` (
  `infohash_id` int(11) NOT NULL,
  `path` varchar(250) NOT NULL,
  `length` bigint(40) NOT NULL,
  KEY `infohash_id` (`infohash_id`)
) ENGINE=InnoDB DEFAULT CHARSET=utf8;

-- 数据导出被取消选择。

-- 导出  表 dhtbt.infohash 结构
CREATE TABLE IF NOT EXISTS `infohash` (
  `id` int(11) NOT NULL AUTO_INCREMENT,
  `infohash` varchar(40) NOT NULL,
  `name` varchar(250) NOT NULL,
  `length` bigint(40) NOT NULL,
  `files` tinyint(1) NOT NULL,
  `addeded` datetime NOT NULL,
  `updated` datetime NOT NULL,
  `cnt` int(11) NOT NULL DEFAULT '0',
  `textindex` mediumtext NOT NULL,
  PRIMARY KEY (`id`),
  KEY `cnt` (`cnt`),
  KEY `updated` (`updated`),
  KEY `addeded` (`addeded`),
  KEY `infohash` (`infohash`),
  FULLTEXT KEY `textindex` (`textindex`)
) ENGINE=InnoDB AUTO_INCREMENT=267277 DEFAULT CHARSET=utf8;

-- 数据导出被取消选择。

/*!40103 SET TIME_ZONE=IFNULL(@OLD_TIME_ZONE, 'system') */;
/*!40101 SET SQL_MODE=IFNULL(@OLD_SQL_MODE, '') */;
/*!40014 SET FOREIGN_KEY_CHECKS=IFNULL(@OLD_FOREIGN_KEY_CHECKS, 1) */;
/*!40101 SET CHARACTER_SET_CLIENT=@OLD_CHARACTER_SET_CLIENT */;
/*!40111 SET SQL_NOTES=IFNULL(@OLD_SQL_NOTES, 1) */;
