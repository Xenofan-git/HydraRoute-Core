import http.server,threading,json
from pathlib import Path
from selenium import webdriver
from selenium.webdriver.chrome.service import Service
from selenium.webdriver.chrome.options import Options
from selenium.webdriver.common.by import By
from selenium.webdriver.support.ui import WebDriverWait
from selenium.webdriver.support import expected_conditions as EC
ROOT=str(Path(__file__).resolve().parents[2] / 'internal/ui/static')
requests=[]
class H(http.server.SimpleHTTPRequestHandler):
 def __init__(self,*args,**kwargs):super().__init__(*args,directory=ROOT,**kwargs)
 def log_message(self,*args):pass
 def do_GET(self):
  if self.path in ('/','/index.html','/app.js','/app.css'):return super().do_GET()
  if self.path.startswith('/v1/') or self.path=='/healthz':
   data={'/v1/runtime':{'running':True,'configured':True},'/v1/geodata/status':{'ready':True,'assets':[{'name':'geoip.dat','available':True},{'name':'geosite.dat','available':True}],'update':{'ready':True,'current':'test-1'}},'/v1/profiles':{'profiles':[]},'/v1/routing':{'routing':{'enabled':False,'rules':[]}},'/v1/events?limit=50':{'events':[]}}.get(self.path,{})
   body=json.dumps(data).encode();self.send_response(200);self.send_header('Content-Type','application/json');self.send_header('Content-Length',str(len(body)));self.end_headers();self.wfile.write(body);return
  self.send_error(404)
 def do_POST(self):
  n=int(self.headers.get('Content-Length',0));body=self.rfile.read(n);requests.append((self.path,json.loads(body)))
  data=json.dumps({'error':'simulated preflight failure'}).encode();self.send_response(422);self.send_header('Content-Type','application/json');self.send_header('Content-Length',str(len(data)));self.end_headers();self.wfile.write(data)
server=http.server.ThreadingHTTPServer(('127.0.0.1',0),H);threading.Thread(target=server.serve_forever,daemon=True).start()
opts=Options();opts.add_argument('--headless=new');opts.add_argument('--no-sandbox');opts.add_argument('--disable-dev-shm-usage');opts.add_argument('--no-proxy-server')
d=webdriver.Chrome(service=Service("/usr/bin/chromedriver"),options=opts)
try:
 d.get('http://127.0.0.1:%d/'%server.server_port)
 wait=WebDriverWait(d,15)
 form=wait.until(EC.presence_of_element_located((By.ID,'geodataUpdateForm')))
 assert d.find_element(By.CSS_SELECTOR,'[data-geodata-action="activate"]').is_enabled()
 d.find_element(By.CSS_SELECTOR,'details.geodata-update summary').click()
 for name,value in [('geoip_url','https://example.com/geoip.dat'),('geoip_sha256','a'*64),('geosite_url','https://example.com/geosite.dat'),('geosite_sha256','b'*64)]:d.find_element(By.NAME,name).send_keys(value)
 d.find_element(By.CSS_SELECTOR,'[data-geodata-action="preflight"]').click()
 wait.until(lambda _:len(requests)>0)
 assert requests[0][0]=='/v1/geodata/preflight',requests
 assert requests[0][1]['geoip']['sha256']=='a'*64
 wait.until(lambda _: 'simulated preflight failure' in d.find_element(By.ID,'toast').text)
 assert d.find_element(By.CSS_SELECTOR,'[data-geodata-action="preflight"]').is_enabled()
 assert not any(p=='/v1/geodata/activate' for p,_ in requests)
 # Browser confirmation is a hard gate before a potentially disruptive update.
 d.execute_script('window.confirm = () => false')
 d.find_element(By.CSS_SELECTOR,'[data-geodata-action="activate"]').click()
 assert not any(p=='/v1/geodata/activate' for p,_ in requests),'cancelled activation reached API'
 d.execute_script('window.confirm = () => true')
 d.find_element(By.CSS_SELECTOR,'[data-geodata-action="activate"]').click()
 wait.until(lambda _:any(p=='/v1/geodata/activate' for p,_ in requests))
 activation=[body for path,body in requests if path=='/v1/geodata/activate']
 assert len(activation)==1 and activation[0]['geosite']['sha256']=='b'*64
 wait.until(lambda _:d.find_element(By.CSS_SELECTOR,'[data-geodata-action="activate"]').is_enabled())
 print('PASS: Chromium rendered GeoData form; preflight JSON correct; server error shown; buttons reenabled; cancelled activation blocked; confirmed activation sent once')
finally:d.quit();server.shutdown()
