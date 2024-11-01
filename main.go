package main

// 原理: 当有请求/cctv13.m3u8时，下载m3u8，读取分析，下载TS切片，使用ffmpeg将ts中的视频去除，修改一下m3u8中的ts路径返回给客户端

// 用法: mpv http://127.0.0.1:8080/cctv13.m3u8

import (
	"fmt"
	"io"
	"log"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"strconv"
	"sync"
	"time"
)

// URL TSName AudioTSName
type TSInfo struct {
	TSURL     string
	TSName    string
	TSNewName string
}
var (
	ListenPort  string = "8080"
	DelTSBefore string = "30s"
	M3U8URL     string = "https://ldncctvwbcdali.v.myalicdn.com/ldncctvwbcd/cdrmldcctv13_1md.m3u8"
	M3U8Name    string = "cctv13.m3u8"
	HttpClient  *FoxHTTPClient
	TsInFoB     []TSInfo
)


type HandlerTSFile struct {
	root string
}

func NewHandlerTSFile(rootDir string) http.Handler {
	return &HandlerTSFile{rootDir}
}

func (sfh *HandlerTSFile) ServeHTTP(w http.ResponseWriter, r *http.Request) {

	fi, err := os.Stat(sfh.root + r.URL.Path)
	if err != nil { // 文件不存在
		http.NotFound(w, r)
		log.Println(r.RemoteAddr, "->", r.RequestURI, ": 不存在 :", r.UserAgent())
		return
	}
	if fi.IsDir() {
		http.NotFound(w, r)
		log.Println(r.RemoteAddr, "->", r.RequestURI, ": 不处理文件夹 :", r.UserAgent())
	} else {
		nowName := strings.ToLower(fi.Name())
		if strings.HasSuffix(nowName, ".ts") {
			http.ServeFile(w, r, sfh.root+r.URL.Path)
			delTS() // 删除一段时间前的ts
			log.Println(r.RemoteAddr, "->", r.Method, r.RequestURI, r.UserAgent())
		} else {
			http.NotFound(w, r)
			log.Println(r.RemoteAddr, "->", r.RequestURI, ": 不处理 :", r.UserAgent())
		}
	}
}

func delTS() { // 删除一段时间前的ts
	currentDir, err := os.Getwd()
	if err != nil {
		fmt.Println("# 错误:", err)
	}

	dSecAgo, err := time.ParseDuration("-" + DelTSBefore)
	if err != nil {
		fmt.Println("# 错误:", err)
	}
	now := time.Now()
	xSecondsAgo := now.Add(dSecAgo)

	err = filepath.Walk(currentDir, func(path string, info os.FileInfo, err error) error {
		if err != nil {
			return err
		}
		if info.IsDir() {
			return nil
		}
		if filepath.Ext(path) == ".ts" && info.ModTime().Before(xSecondsAgo) {
			err := os.Remove(path)
			if err != nil {
				fmt.Printf("- 删除文件 %s 失败: %v\n", path, err)
			} else {
//				fmt.Printf("- 已删除文件 %s\n", path)
			}
		}
		return nil
	})

	if err != nil {
		fmt.Println("# 错误:", err)
	}
}

func NewHandlerM3U8(w http.ResponseWriter, r *http.Request) {
	log.Println(r.RemoteAddr, "->", r.Method, r.RequestURI, r.UserAgent())
	if "POST" == r.Method {
		return
	}
	if "GET" == r.Method {

		// 下载M3U8URL，分析下载ts，ffmpeg ts -> ts，修改m3u8内容(ts文件路径)
		oldM3U8Name := HttpClient.getTS(M3U8URL, "")
		m3u8Content := FileRead(oldM3U8Name)
		os.Remove(oldM3U8Name)

		// 分析m3u8
		TsInFoB = getTSInfoList(m3u8Content, M3U8URL)
		tsCount := len(TsInFoB)

		// 分配任务 下载/转换
		var wg sync.WaitGroup
		for i := 0; i < tsCount; i++ {
			wg.Add(1)
			go func(thNum int) {
				defer wg.Done()
				if ! FileExist(TsInFoB[thNum].TSNewName) {
					fmt.Println("-", thNum, "debug: 下载转换:", TsInFoB[thNum].TSName)
					HttpClient.getTS(TsInFoB[thNum].TSURL, TsInFoB[thNum].TSName)
					_, err := exec.LookPath("ffmpeg")
					if err != nil {
						fmt.Println("木有找到ffmpeg: ", err)
					} else {
						exec.Command("ffmpeg", "-i", TsInFoB[thNum].TSName, "-vn", "-c:a", "copy", TsInFoB[thNum].TSNewName).Output()
						os.Remove(TsInFoB[thNum].TSName)
					}
				} else {
					fmt.Println("-", thNum, "debug: 已经存在:", TsInFoB[thNum].TSName)
				}
			}(i)
		}
		wg.Wait() // 等待完毕

		// 返回:新m3u8
		w.Header().Add("Content-Type", "application/vnd.apple.mpegurl")
		w.WriteHeader(200)
		fmt.Fprint(w, getNewM3u8(m3u8Content, M3U8URL))
	}
}

func getNewM3u8(iM3U8 string, iM3U8URL string) string {
	var oStr []string
	lines := strings.Split(iM3U8, "\n")
	for _, line := range lines {
		if !strings.Contains(line, "#EXT") {
			if len(strings.ReplaceAll(line, " ", "")) > 1 {
				tsURL := GetFullURL(strings.ReplaceAll(line, "\r", ""), iM3U8URL)
				tsName := GetFileNameOfURL(tsURL)
				newTSName := "a" + tsName
				oStr = append(oStr, newTSName)
			}
		} else {
			oStr = append(oStr, line)
		}
	}
	return strings.Join(oStr, "\n")
}

func getTSInfoList(iM3U8 string, iM3U8URL string) []TSInfo {
	var AInfo []TSInfo
	lines := strings.Split(iM3U8, "\n")
	for _, line := range lines {
		if !strings.Contains(line, "#EXT") {
			if len(strings.ReplaceAll(line, " ", "")) > 1 {
				tsURL := GetFullURL(strings.ReplaceAll(line, "\r", ""), iM3U8URL)
				tsName := GetFileNameOfURL(tsURL)
				newTSName := "a" + tsName
//				fmt.Println("- debug:", tsURL, tsName, newTSName)
				AInfo = append(AInfo, TSInfo{tsURL, tsName, newTSName})
			}
		}
	}
	return AInfo
}

func init() {
	HttpClient = NewFoxHTTPClient()
}

func main() {

	fmt.Println("# Port:", ListenPort, "            PID:", os.Getpid())

	srv := &http.Server{Addr: ":" + ListenPort}

	http.Handle("/", NewHandlerTSFile(".")) // ./*.ts文件处理

	http.HandleFunc("/" + M3U8Name, NewHandlerM3U8)

	if err := srv.ListenAndServe(); err != nil {
		fmt.Fprintln(os.Stderr, "ListenAndServe: ", err)
	}

}

func FileExist(filename string) bool {
	_, err := os.Stat(filename)
	return err == nil || os.IsExist(err)
}

func FileRead(iPath string) string {
	bytes, err := os.ReadFile(iPath)
	if err != nil {
		fmt.Println("- Error @ FileRead() :", err)
		return ""
	}
	return string(bytes)
}

type FoxHTTPClient struct {
	httpClient *http.Client
}

func NewFoxHTTPClient() *FoxHTTPClient {
	tOut, _ := time.ParseDuration(fmt.Sprintf("%ds", 9))
	return &FoxHTTPClient{httpClient: &http.Client{Transport: &http.Transport{Proxy: http.ProxyFromEnvironment, MaxIdleConnsPerHost: 9}, Timeout: tOut}}
}

func (fhc *FoxHTTPClient) getTS(iURL string, savePath string) string {
	req, _ := http.NewRequest("GET", iURL, nil)
	req.Header.Set("User-Agent", "ie11")
	req.Header.Set("Connection", "keep-alive")

	response, err := fhc.httpClient.Do(req)
	if nil != err {
		fmt.Println("- Error @ getTS() :", err)
		return ""
	}
	defer response.Body.Close()

	if "" == savePath {
		savePath = GetFileNameOfURL(iURL)
	}
	f, _ := os.OpenFile(savePath, os.O_RDWR|os.O_CREATE, 0666)
	defer f.Close()
	writeLen, err := io.Copy(f, response.Body)
	if err != nil {
		fmt.Println("- Error @ getTS() io.Copy():", err)
		return ""
	}
	response.Body.Close()
	f.Close()
	hLen := response.Header.Get("Content-Length")
	if "" != hLen {
		if hLen == strconv.FormatInt(writeLen, 10) {
			chFileLastModified(savePath, response.Header.Get("Last-Modified"))
		} else {
			fmt.Println("- Error @ getTS() 文件未下载完毕 :", savePath)
			return ""
		}
	} else {
		chFileLastModified(savePath, response.Header.Get("Last-Modified"))
	}
	return savePath
}

func GetFileNameOfURL(iURL string) string {
	uu, _ := url.Parse(iURL)
	return filepath.Base(uu.Path)
}

func GetFullURL(subURL, baseURL string) string {
	bu, _ := url.Parse(baseURL)
	pu, _ := bu.Parse(subURL)
	return pu.String()
}

func chFileLastModified(filePath string, fileLastModified string) { // "Last-Modified"
	if "" != fileLastModified {
		myLoc, _ := time.LoadLocation("Asia/Shanghai")
		mtime, _ := time.ParseInLocation(time.RFC1123, fileLastModified, myLoc)
		atime, _ := time.Parse(time.RFC1123, fileLastModified)
		if err := os.Chtimes(filePath, atime, mtime); err != nil {
			fmt.Println("- Error @ chFileLastModified() :", err)
		}
	}
}

