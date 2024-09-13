
# 时间敏感的 DNS Proxy

为中国大陆专业玩家修改的 dnsproxy, 借助并合理利用低延迟的国际 DNS 访问渠道

## Why

现有的 DNS 防污染/优化/分流/加速方案没有考虑影响用户体验的参数--耐心(时间)  

本 dnsproxy fork 将时间作为重要依据, 适合的用户场景如下

1. 软件发起 TCP 请求到代理, 代理首先判断域名在列表中, **只有**大陆域名和无法判断的域名, 比如一个小众域名, **才**需要 DNS 解析
2. 代理向本 dnsproxy 发起解析, 解析结果如果是大陆 IP 则直接连接, 否则使用**域名**继续向其他代理发起连接
 >  DNS 污染从未有过将域名污染到大陆 IP, 因此 dnsproxy 无需处理 DNS 污染
 >  代理会使用**域名**继续向其他代理发起连接, 因此 dnsproxy 无需分流, 无需判断 IP     国家


在以上典型流程中, dnsproxy 一般被配置为并发向多个上游发起请求, 一般是一组国内一组国外  
但专业用户往往使用隧道将海外 DNS 服务以低延迟的方式暴露成本地服务 
这种情况下, 会出现海外 DNS 服务抢先返回了结果, 导致不必要的连接通过代理, 尤其是 CDN  域名

为了避免这种情况, 本 dnsproxy fork 使用了以下逻辑

## 原理

以参数 `--upstream-mode=parallel -u 192.168.0.53:53 -u https://223.5.5.5/dns-query` 为例, 这里 192.168.0.53:53 是一个低延迟的海外 DNS

使用环境变量 `LOCAL-DNS-PATTERN` 用于区分哪个上游是本地(大陆) DNS, 这里可以设置为 `*223.5.5.5*` 
使用环境变量 `LOCAL-DNS-TOLERANCE-MS` 用于设置容忍时间(毫秒), 在容忍时间内, 海外 DNS 的结果和空结果会被暂存

dnsproxy 收到请求后并发向上游发起请求, 可以理解成赛跑, 在容忍时间内, 胜出的一方作为结果返回; 容忍时间后, 不再挑剔结果

如何判断胜出:  
1. 容忍时间内, 大陆 DNS 返回了非空结果, 则胜出
2. 容忍时间内, 一方返回了非空结果, 另一方返回了空结果, 非空的胜出
3. 超过容忍时间, 采取可用的任意结果

容忍机制使得大陆 DNS 有更充裕的时间响应一个大陆 IP, 一定程度避免了国内域名使用海外 DNS 的海外 IP 响应

## 上手

下载代码编译, 启动 dnsproxy; 或者也下载 `https://github.com/AdguardTeam/AdGuardHome` 使用 `go mod edit -replace github.com/AdguardTeam/dnsproxy=/path/to/dnsproxy` 替换路径后编译 AdGuardHome, 启动 AdGuardHome

可以分别使用大陆域名, 大陆域名不存在的子域名, 海外域名(尤其是 NS 为 cloudflare 的域名), 海外域名不存在的子域名进行测试, 返回延迟应当符合预期

为了避免缓存影响, 可以使用泛域名每次随机或者数字 +1, 如 `random42.dns.nextdns.io` `random42.alidns.com` `random42.doh.360.cn` `random42.doh.pub` `192-168-0-1.sslip.io`

```
1 [info] dnsproxy: use local Answer q=baidu.com. duration=13.99ms u=https://223.5.5.5:443/dns-query ttl=190

2 [info] dnsproxy: defer NEG Answer q=nxdomain42.baidu.com. duration=22.87ms u=https://223.5.5.5:443/dns-query
3 [info] dnsproxy: NEG Answer from both DNS q=nxdomain42.baidu.com. duration=46.11ms u=192.168.0.53:53

4 [info] dnsproxy: defer NEG Answer q=nxdomain42.cloudflare.com. duration=32.9ms u=192.168.0.53:53
5 [info] dnsproxy: tolerance Timeout, use deferred q=nxdomain42.cloudflare.com. duration=60.34ms u=192.168.0.53:53

6 [info] dnsproxy: defer non local Answer q=bbs.125.la. duration=25.88ms u=192.168.0.53:53
7 [info] dnsproxy: use local Answer q=bbs.125.la. duration=55.5ms u=https://120.53.53.53:443/dns-query ttl=120
```

按行说明

1. 大陆域名, 大陆 DNS 最先返回结果并采纳, 符合预期
2. 大陆域名但 NXDOMAIN, 由于仍有耐心, 等待, 符合预期
3. 海外 DNS 也返回 NXDOMAIN, 由于大陆和海外均返回空结果, 采纳, 符合预期
4. 海外域名但 NXDOMAIN, 由于仍有耐心, 等待, 符合预期
5. 失去耐心等待大陆 DNS 结果, 使用海外 DNS 的空结果, 符合预期
6. 大陆域名, 但海外 DNS 先返回结果,由于仍有耐心, 等待, 符合预期
7. 大陆域名, 大陆 DNS 在耐心结束前返回了结果, 采纳, 符合预期

不想看到的情况

```
1 [info] dnsproxy: defer non local Answer q=baidu.com. duration=51.45ms u=192.168.0.53:53
2 [info] dnsproxy: tolerance Timeout, use deferred q=baidu.com. duration=79.89ms ttl=192 u=192.168.0.53:53
```

按行说明
1. 海外 DNS 抢先返回了大陆域名的结果, 由于仍有耐心, 等待, 符合预期
2. 失去耐心等待大陆 DNS 结果, 使用海外 DNS 的结果, 但预期该域名应当由大陆 DNS 快速响应, 不符合预期

这种情况可以
1. 确认大陆 DNS 上游延迟
2. 考虑添加更优的大陆 DNS 上游
3. 增加容忍时间



## 效果评价

### 功能生效率
可以使用具体的大陆域名列表进行测试  
符合先 `defer non local Answer` 后 `use local Answer` 的属于功能生效;  
符合先 `defer non local Answer` 后 `tolerance Timeout` 的属于功能未起作用,可能需要调整;  
符合`use local Answer` 的属于无需本功能介入.

### 综合延迟表现
// todo dnsperf


