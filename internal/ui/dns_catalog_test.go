package ui

import (
 "encoding/json"
 "net/http/httptest"
 "net/url"
 "testing"
)

func TestDNSCatalogHasValidUniqueDoHProviders(t *testing.T) {
 w:=httptest.NewRecorder();Handler().ServeHTTP(w,httptest.NewRequest("GET","/dns-catalog.json",nil))
 if w.Code!=200 {t.Fatalf("catalog status %d",w.Code)}
 var c struct{Providers []struct{Name string `json:"name"`; Types map[string]string `json:"types"`} `json:"providers"`}
 if err:=json.Unmarshal(w.Body.Bytes(),&c);err!=nil {t.Fatal(err)}
 if len(c.Providers)<80 {t.Fatalf("only %d providers",len(c.Providers))}
 names:=map[string]bool{};addresses:=map[string]bool{}
 for _,p:=range c.Providers {
  if p.Name==""||names[p.Name] {t.Fatalf("invalid or duplicate name %q",p.Name)}
  names[p.Name]=true
  address:=p.Types["DOH"];u,err:=url.Parse(address)
  if err!=nil||u.Scheme!="https"||u.Host==""||u.Path==""||addresses[address] {t.Fatalf("invalid or duplicate DoH %q",address)}
  addresses[address]=true
 }
}
