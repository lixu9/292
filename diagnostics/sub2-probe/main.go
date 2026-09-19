package main

import (
 "bufio"
 "context"
 "encoding/json"
 "flag"
 "fmt"
 "net/http"
 "os"
 "sync"
 "time"

 "github.com/Wei-Shaw/sub2api/internal/repository"
 "github.com/Wei-Shaw/sub2api/internal/service"
)

type observedUpstream struct { service.HTTPUpstream; protocol string }
func (u *observedUpstream) Do(req *http.Request, proxyURL string, id int64, concurrency int) (*http.Response,error) {
 resp,err:=u.HTTPUpstream.Do(req,proxyURL,id,concurrency)
 if resp!=nil { u.protocol=resp.Proto }
 return resp,err
}

func main() {
 authPath:=flag.String("auth-file","","existing account auth file")
 proxyURL:=flag.String("proxy","socks5h://127.0.0.1:7890","dedicated harvest proxy")
 version:=flag.String("version","0.155.1","Sub2 runtime client version")
 waitForStart:=flag.Bool("wait-for-start",false,"signal readiness and wait for stdin before probing")
 flag.Parse()
 b,err:=os.ReadFile(*authPath); if err!=nil {panic(err)}
 var auth map[string]interface{}
 if err=json.Unmarshal(b,&auth);err!=nil {panic(err)}
 token,ok:=auth["access_token"].(string);if !ok {panic("missing access token")}
 accountID,ok:=auth["account_id"].(string);if !ok {panic("missing account identity")}
 service.SetCodexCanonicalUserAgentResolver(func()string{return "codex-tui/"+*version+" (Ubuntu 22.4.0; x86_64) xterm-256color"})
 account:=&service.Account{ID:2,Platform:"openai",Type:"oauth",Concurrency:10,Credentials:map[string]interface{}{"chatgpt_account_id":accountID}}
 if *waitForStart {
  fmt.Println(`{"ready":true}`)
  if _,err:=bufio.NewReader(os.Stdin).ReadString('\n');err!=nil {panic("start signal unavailable")}
 }
 var wg sync.WaitGroup
 for _,model:=range []string{"gpt-6-astra","gpt-5.6-sol"} {
  wg.Add(1)
  go func(model string) {
   defer wg.Done()
   upstream:=&observedUpstream{HTTPUpstream:repository.NewHTTPUpstream(nil)}
   started:=time.Now()
   state,status,err:=service.DiagnosticCodex292Probe(context.Background(),upstream,account,token,model,*proxyURL)
   result:=map[string]interface{}{"implementation":"sub2_original","model":model,"http":status,"length":len(state),"protocol":upstream.protocol,"startedAt":started.UnixMilli(),"durationMs":time.Since(started).Milliseconds()}
   if err!=nil {result["error"]="connection_failed"}
   b,_:=json.Marshal(result);fmt.Println(string(b))
  }(model)
 }
 wg.Wait()
}
