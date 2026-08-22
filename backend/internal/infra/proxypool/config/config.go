package config

// ResourceRoot 返回 GeoIP 资源文件目录。proxypool 原版从外部配置读取,
// 这里返回空字符串让 geoIp 包使用默认路径或跳过 GeoIP 查询。
var ResourceRoot = ""
