package dnsservice

import (
	"crypto/tls"
	"encoding/json"
	"fmt"
	mdns "github.com/miekg/dns"
	"io"
	"net"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	goruntime "runtime"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"
)

type Server struct {
	Name      string `json:"name"`
	Protocol  string `json:"protocol"`
	Address   string `json:"address"`
	Enabled   bool   `json:"enabled"`
	LatencyMS int64  `json:"latency_ms,omitempty"`
	Features  string `json:"features,omitempty"`
}
type Host struct {
	Domain  string `json:"domain"`
	Address string `json:"address"`
}
type Blocklist struct {
	Name    string `json:"name"`
	URL     string `json:"url"`
	Entries int    `json:"entries"`
	Enabled bool   `json:"enabled"`
}
type Rule struct {
	Domain string `json:"domain"`
	Action string `json:"action"`
}
type Config struct {
	RuleDNS           map[string][]string `json:"rule_dns,omitempty"`
	StatisticsEnabled bool                `json:"statistics_enabled"`
	Enabled           bool                `json:"enabled"`
	Listen            string              `json:"listen"`
	Port              int                 `json:"port"`
	AutoProvider      bool                `json:"auto_provider"`
	ProviderID        string              `json:"provider_id"`
	ProviderName      string              `json:"provider_name"`
	ProviderServers   []string            `json:"provider_servers"`
	DefaultProvider   string              `json:"default_provider"`
	Servers           []Server            `json:"servers"`
	Hosts             []Host              `json:"hosts"`
	Blocklists        []Blocklist         `json:"blocklists"`
	Rules             []Rule              `json:"rules"`
	BlockingEnabled   bool                `json:"blocking_enabled"`
}
type Stats struct {
	MemoryMB     float64 `json:"memory_mb"`
	Total        uint64  `json:"total"`
	Blocked      uint64  `json:"blocked"`
	Success      uint64  `json:"success"`
	Errors       uint64  `json:"errors"`
	AvgLatencyMS int64   `json:"avg_latency_ms"`
	Provider     string  `json:"provider"`
}
type Manager struct {
	mu                                              sync.RWMutex
	path                                            string
	cfg                                             Config
	udp, tcp                                        *mdns.Server
	serveDone                                       chan struct{}
	generation                                      uint64
	running                                         bool
	total, blocked, success, errors, latency, count atomic.Uint64
	hosts                                           map[string]net.IP
	client                                          *http.Client
}

func DefaultConfig() Config {
	return Config{Listen: "127.0.0.1", Port: 5353, AutoProvider: true, Servers: []Server{{Name: "Yandex DNS · Basic", Protocol: "DoH", Address: "https://common.dot.dns.yandex.net/dns-query", Enabled: true}, {Name: "AdGuard DNS · Default", Protocol: "DoH", Address: "https://dns.adguard-dns.com/dns-query", Enabled: true}, {Name: "Cloudflare · 1.1.1.1", Protocol: "DoH", Address: "https://cloudflare-dns.com/dns-query", Enabled: true}, {Name: "Quad9 · Standard", Protocol: "DoH", Address: "https://dns.quad9.net/dns-query", Enabled: true}}}
}
func New(path string) *Manager {
	m := &Manager{path: path, client: &http.Client{Timeout: 8 * time.Second, CheckRedirect: func(req *http.Request, via []*http.Request) error {
		if req.URL.Scheme != "https" {
			return fmt.Errorf("DoH redirect must remain HTTPS")
		}
		if len(via) >= 5 {
			return fmt.Errorf("too many DoH redirects")
		}
		return nil
	}}, hosts: map[string]net.IP{}}
	m.cfg = DefaultConfig()
	_ = m.Load()
	return m
}
func (m *Manager) Load() error {
	b, e := os.ReadFile(m.path)
	if os.IsNotExist(e) {
		return m.Save(m.cfg)
	}
	if e != nil {
		return e
	}
	var c Config
	if e = json.Unmarshal(b, &c); e != nil {
		return e
	}
	if c.Listen == "" {
		c.Listen = "127.0.0.1"
	}
	if c.Port == 0 {
		c.Port = 5353
	}
	m.mu.Lock()
	m.cfg = c
	m.hosts = map[string]net.IP{}
	for _, h := range c.Hosts {
		if ip := net.ParseIP(h.Address); ip != nil {
			m.hosts[strings.ToLower(strings.TrimSuffix(h.Domain, "."))] = ip
		}
	}
	m.mu.Unlock()
	return nil
}
func (m *Manager) Config() Config { m.mu.RLock(); defer m.mu.RUnlock(); return m.cfg }
func (m *Manager) Save(c Config) error {
	if c.Listen == "" {
		c.Listen = "127.0.0.1"
	}
	if c.Port < 1024 || c.Port > 65535 {
		return fmt.Errorf("DNS port must be 1024-65535")
	}
	ip := net.ParseIP(c.Listen)
	if ip == nil || !ip.IsLoopback() {
		return fmt.Errorf("DNS listener must be loopback; LAN exposure is not enabled")
	}
	for _, s := range c.Servers {
		p := strings.ToUpper(s.Protocol)
		if p != "DO53" && p != "DOT" && p != "DOH" {
			return fmt.Errorf("unsupported DNS protocol %q", s.Protocol)
		}
		if s.Address == "" || strings.ContainsAny(s.Address, " \t\r\n") {
			return fmt.Errorf("DNS server %q has no valid address", s.Name)
		}
		if p == "DOH" {
			u, err := url.Parse(s.Address)
			if err != nil || u.Scheme != "https" || u.Host == "" || u.User != nil || u.Fragment != "" {
				return fmt.Errorf("DNS server %q must use a valid HTTPS DoH URL", s.Name)
			}
		}
	}
	b, e := json.MarshalIndent(c, "", "  ")
	if e != nil {
		return e
	}
	if e = os.MkdirAll(filepath.Dir(m.path), 0700); e != nil {
		return e
	}
	tmp, e := os.CreateTemp(filepath.Dir(m.path), ".dns-*")
	if e != nil {
		return e
	}
	n := tmp.Name()
	defer os.Remove(n)
	if e = tmp.Chmod(0600); e != nil {
		tmp.Close()
		return e
	}
	if _, e = tmp.Write(append(b, '\n')); e != nil {
		tmp.Close()
		return e
	}
	if e = tmp.Sync(); e != nil {
		tmp.Close()
		return e
	}
	if e = tmp.Close(); e != nil {
		return e
	}
	if e = os.Rename(n, m.path); e != nil {
		return e
	}
	m.mu.Lock()
	m.cfg = c
	m.hosts = map[string]net.IP{}
	for _, h := range c.Hosts {
		if ip := net.ParseIP(h.Address); ip != nil {
			m.hosts[strings.ToLower(strings.TrimSuffix(h.Domain, "."))] = ip
		}
	}
	m.mu.Unlock()
	return nil
}
func (m *Manager) Status() map[string]any {
	m.mu.RLock()
	defer m.mu.RUnlock()
	return map[string]any{"running": m.running, "listen": m.cfg.Listen, "port": m.cfg.Port, "provider_name": m.cfg.ProviderName, "provider_servers": m.cfg.ProviderServers}
}
func (m *Manager) Stats() Stats {
	c := m.Config()
	n := m.count.Load()
	var avg uint64
	if n > 0 {
		avg = m.latency.Load() / n
	}
	var mem goruntime.MemStats
	goruntime.ReadMemStats(&mem)
	return Stats{MemoryMB: float64(mem.Alloc) / (1024 * 1024), Total: m.total.Load(), Blocked: m.blocked.Load(), Success: m.success.Load(), Errors: m.errors.Load(), AvgLatencyMS: int64(avg), Provider: c.ProviderName}
}
func (m *Manager) Start() error {
	m.mu.Lock()
	if m.running || m.udp != nil || m.tcp != nil {
		m.mu.Unlock()
		return fmt.Errorf("DNS service already running or shutting down")
	}
	addr := net.JoinHostPort(m.cfg.Listen, fmt.Sprint(m.cfg.Port))
	mux := mdns.NewServeMux()
	mux.HandleFunc(".", m.handle)
	pc, err := net.ListenPacket("udp", addr)
	if err != nil {
		m.mu.Unlock()
		return fmt.Errorf("DNS UDP listener: %w", err)
	}
	ln, err := net.Listen("tcp", addr)
	if err != nil {
		_ = pc.Close()
		m.mu.Unlock()
		return fmt.Errorf("DNS TCP listener: %w", err)
	}
	u := &mdns.Server{PacketConn: pc, Handler: mux}
	t := &mdns.Server{Listener: ln, Handler: mux}
	m.generation++
	generation := m.generation
	done := make(chan struct{})
	m.udp, m.tcp, m.serveDone = u, t, done
	m.running = true

	var wg sync.WaitGroup
	wg.Add(2)
	startErr := make(chan error, 2)
	launch := func(server *mdns.Server, peer *mdns.Server) {
		ready := make(chan struct{})
		var readyOnce sync.Once
		server.NotifyStartedFunc = func() { readyOnce.Do(func() { close(ready) }) }
		go func() {
			defer wg.Done()
			err := server.ActivateAndServe()
			if err != nil {
				startErr <- err
			}
			readyOnce.Do(func() { close(ready) })
			if err != nil {
				m.stopAfterServeFailure(generation, peer)
			}
		}()
		<-ready
	}
	launch(u, t)
	launch(t, u)
	go func() {
		wg.Wait()
		close(done)
	}()
	select {
	case err := <-startErr:
		m.mu.Unlock()
		_ = m.Stop()
		return fmt.Errorf("DNS server failed during startup: %w", err)
	default:
	}
	m.mu.Unlock()
	return nil
}

func (m *Manager) stopAfterServeFailure(generation uint64, peer *mdns.Server) {
	m.mu.Lock()
	if m.generation == generation {
		m.running = false
	}
	m.mu.Unlock()
	if err := peer.Shutdown(); err != nil {
		if peer.PacketConn != nil {
			_ = peer.PacketConn.Close()
		}
		if peer.Listener != nil {
			_ = peer.Listener.Close()
		}
	}
}

func (m *Manager) Stop() error {
	m.mu.Lock()
	u, t, done := m.udp, m.tcp, m.serveDone
	m.running = false
	m.mu.Unlock()
	var first error
	if u != nil {
		if err := u.Shutdown(); err != nil {
			if u.PacketConn != nil {
				_ = u.PacketConn.Close()
			}
			if !strings.Contains(err.Error(), "server not started") {
				first = err
			}
		}
	}
	if t != nil {
		if err := t.Shutdown(); err != nil {
			if t.Listener != nil {
				_ = t.Listener.Close()
			}
			if !strings.Contains(err.Error(), "server not started") && first == nil {
				first = err
			}
		}
	}
	if done != nil {
		<-done
	}
	m.mu.Lock()
	if m.serveDone == done {
		m.udp, m.tcp, m.serveDone = nil, nil, nil
	}
	m.mu.Unlock()
	return first
}
func (m *Manager) handle(w mdns.ResponseWriter, r *mdns.Msg) {
	start := time.Now()
	trackStats := m.Config().StatisticsEnabled
	if trackStats {
		m.total.Add(1)
	}
	reply := new(mdns.Msg)
	reply.SetReply(r)
	blocked := false
	for _, q := range r.Question {
		host := strings.TrimSuffix(strings.ToLower(q.Name), ".")
		m.mu.RLock()
		ip := m.hosts[host]
		m.mu.RUnlock()
		if ip != nil {
			if q.Qtype == mdns.TypeA && ip.To4() != nil {
				rr, _ := mdns.NewRR(fmt.Sprintf("%s 60 IN A %s", q.Name, ip.To4()))
				reply.Answer = append(reply.Answer, rr)
				continue
			}
			if q.Qtype == mdns.TypeAAAA && ip.To4() == nil {
				rr, _ := mdns.NewRR(fmt.Sprintf("%s 60 IN AAAA %s", q.Name, ip))
				reply.Answer = append(reply.Answer, rr)
				continue
			}
			if q.Qtype == mdns.TypeA || q.Qtype == mdns.TypeAAAA {
				continue
			}
		}
		if m.blockedHost(host) {
			blocked = true
			if trackStats {
				m.blocked.Add(1)
			}
			if q.Qtype == mdns.TypeA {
				rr, _ := mdns.NewRR(q.Name + " 60 IN A 0.0.0.0")
				reply.Answer = append(reply.Answer, rr)
			}
		}
	}
	if !blocked && len(reply.Answer) == 0 {
		if e := m.forward(r, reply); e != nil {
			reply.Rcode = mdns.RcodeServerFailure
			if trackStats {
				m.errors.Add(1)
			}
		} else {
			if trackStats {
				m.success.Add(1)
			}
		}
	}
	if trackStats {
		m.latency.Add(uint64(time.Since(start).Milliseconds()))
		m.count.Add(1)
	}
	_ = w.WriteMsg(reply)
}
func (m *Manager) blockedHost(host string) bool {
	c := m.Config()
	if !c.BlockingEnabled {
		return false
	}
	for _, r := range c.Rules {
		d := strings.TrimPrefix(strings.ToLower(r.Domain), "*.")
		if host == d || strings.HasSuffix(host, "."+d) {
			return strings.ToLower(r.Action) != "allow"
		}
	}
	return false
}
func (m *Manager) forward(req, resp *mdns.Msg) error {
	c := m.Config()
	var candidates []Server
	if c.AutoProvider {
		for _, ip := range DetectProviderDNS() {
			candidates = append(candidates, Server{Name: "DNS активного DHCP", Protocol: "Do53", Address: net.JoinHostPort(ip, "53"), Enabled: true})
		}
	}
	for i := range c.Servers {
		s := c.Servers[i]
		if !s.Enabled {
			continue
		}
		if c.DefaultProvider != "" && !strings.EqualFold(c.DefaultProvider, s.Name) {
			continue
		}
		candidates = append(candidates, s)
	}
	if len(candidates) == 0 && c.DefaultProvider != "" {
		for i := range c.Servers {
			if c.Servers[i].Enabled {
				candidates = append(candidates, c.Servers[i])
			}
		}
	}
	if len(candidates) == 0 {
		return fmt.Errorf("no active DNS upstream")
	}
	var last error
	for _, server := range candidates {
		if err := m.exchange(req, resp, server); err == nil {
			return nil
		} else {
			last = fmt.Errorf("%s: %w", server.Name, err)
		}
	}
	return fmt.Errorf("all DNS upstreams failed: %w", last)
}

func (m *Manager) exchange(req, resp *mdns.Msg, s Server) error {
	q := req.Copy()
	p := strings.ToUpper(s.Protocol)
	if p == "DOH" {
		u, err := url.Parse(s.Address)
		if err != nil || u.Scheme != "https" || u.Host == "" || u.User != nil || u.Fragment != "" {
			return fmt.Errorf("DoH endpoint must be a valid HTTPS URL")
		}
		b, err := q.Pack()
		if err != nil {
			return err
		}
		h, err := http.NewRequest("POST", s.Address, strings.NewReader(string(b)))
		if err != nil {
			return err
		}
		h.Header.Set("Content-Type", "application/dns-message")
		h.Header.Set("Accept", "application/dns-message")
		res, err := m.client.Do(h)
		if err != nil {
			return err
		}
		defer res.Body.Close()
		if res.StatusCode != http.StatusOK {
			return fmt.Errorf("DoH HTTP %d", res.StatusCode)
		}
		b, err = io.ReadAll(io.LimitReader(res.Body, 65536))
		if err != nil {
			return err
		}
		if err = resp.Unpack(b); err != nil {
			return err
		}
		resp.Id = req.Id
		return nil
	}
	network, port := "udp", "53"
	if p == "DOT" {
		network, port = "tcp-tls", "853"
	}
	address := s.Address
	if _, _, err := net.SplitHostPort(address); err != nil {
		address = net.JoinHostPort(address, port)
	}
	cl := &mdns.Client{Net: network, Timeout: 5 * time.Second}
	if p == "DOT" {
		host, _, _ := net.SplitHostPort(address)
		cl.TLSConfig = &tls.Config{MinVersion: tls.VersionTLS12, ServerName: host}
	}
	a, _, err := cl.Exchange(q, address)
	if err != nil {
		return err
	}
	if p == "DO53" && a.Truncated {
		tcpClient := &mdns.Client{Net: "tcp", Timeout: 5 * time.Second}
		a, _, err = tcpClient.Exchange(q, address)
		if err != nil {
			return fmt.Errorf("DNS TCP retry after truncated UDP response: %w", err)
		}
	}
	*resp = *a
	resp.Id = req.Id
	return nil
}

func parseDHCPDNSServers(output string) []string {
	seen := map[string]bool{}
	var out []string
	for _, line := range strings.FieldsFunc(output, func(r rune) bool { return r == '\n' || r == '|' }) {
		line = strings.TrimSpace(line)
		key, value, ok := strings.Cut(line, "=")
		if !ok || !strings.Contains(strings.ToLower(key), "domain_name_servers") {
			continue
		}
		for _, field := range strings.FieldsFunc(value, func(r rune) bool {
			return r == ',' || r == ';' || r == ' ' || r == '\t'
		}) {
			ip := net.ParseIP(strings.TrimSpace(field))
			if ip == nil || ip.IsLoopback() || ip.IsUnspecified() || ip.IsLinkLocalUnicast() {
				continue
			}
			v := ip.String()
			if !seen[v] {
				seen[v] = true
				out = append(out, v)
			}
		}
	}
	return out
}

// DetectProviderDNS returns only DNS servers explicitly supplied by DHCP on
// the interface carrying the active IPv4 default route. It intentionally
// does not read resolv.conf: that file can contain manual, VPN, or fallback DNS
// and therefore cannot identify the ISP/DHCP-provided resolver.
func defaultRouteInterfaceFromProc(data string) string {
	bestMetric := uint64(^uint64(0))
	best := ""
	lines := strings.Split(data, "\n")
	for i, line := range lines {
		if i == 0 {
			continue
		}
		f := strings.Fields(line)
		if len(f) < 8 || f[1] != "00000000" {
			continue
		}
		flags, e1 := strconv.ParseUint(f[3], 16, 64)
		metric, e2 := strconv.ParseUint(f[6], 10, 64)
		if e1 != nil || e2 != nil || flags&3 != 3 || f[0] == "lo" {
			continue
		}
		if metric < bestMetric {
			bestMetric, best = metric, f[0]
		}
	}
	return best
}

// DetectProviderDNS returns only DNS servers explicitly supplied by DHCP on
// the interface carrying the active IPv4 default route. It intentionally
// avoids resolv.conf and NetworkManager's merged IP4.DNS field, which can
// contain manual, VPN, or fallback resolvers.
func DetectProviderDNS() []string {
	route, err := exec.Command("/usr/sbin/ip", "-4", "route", "show", "default").Output()
	dev := ""
	if err == nil {
		fields := strings.Fields(string(route))
		for i := 0; i+1 < len(fields); i++ {
			if fields[i] == "dev" {
				dev = fields[i+1]
				break
			}
		}
	}
	// `ip route` may be unavailable inside a hardened systemd service's
	// sandbox. The kernel route table is read-only and provides the same
	// default-interface selection without changing any network state.
	if dev == "" {
		if b, e := os.ReadFile("/proc/net/route"); e == nil {
			dev = defaultRouteInterfaceFromProc(string(b))
		}
	}
	if dev == "" {
		return nil
	}
	for _, args := range [][]string{
		{"-g", "DHCP4.OPTION", "device", "show", dev},
		{"-g", "DHCP6.OPTION", "device", "show", dev},
	} {
		b, e := exec.Command("/usr/bin/nmcli", args...).CombinedOutput()
		if e != nil {
			continue
		}
		if ips := parseDHCPDNSServers(string(b)); len(ips) > 0 {
			return ips
		}
	}
	return nil
}

func (m *Manager) Detect() map[string]any {
	ips := DetectProviderDNS()
	name := "DNS оператора не определён"
	if len(ips) > 0 {
		name = "DNS получен из DHCP активного подключения"
	}
	return map[string]any{"provider_name": name, "provider_servers": ips}
}
func (m *Manager) Check(domain string) ([]string, error) {
	domain = strings.TrimSuffix(strings.TrimSpace(domain), ".")
	if domain == "" || len(domain) > 253 || strings.ContainsAny(domain, "/\\\r\n \t") {
		return nil, fmt.Errorf("invalid domain")
	}
	var out []string
	var last error
	for _, qtype := range []uint16{mdns.TypeA, mdns.TypeAAAA} {
		q := new(mdns.Msg)
		q.SetQuestion(mdns.Fqdn(domain), qtype)
		resp := new(mdns.Msg)
		if err := m.forward(q, resp); err != nil {
			last = err
			continue
		}
		for _, answer := range resp.Answer {
			if rr, ok := answer.(*mdns.A); ok {
				out = append(out, rr.A.String())
			} else if rr, ok := answer.(*mdns.AAAA); ok {
				out = append(out, rr.AAAA.String())
			}
		}
	}
	if len(out) == 0 && last != nil {
		return nil, last
	}
	return out, nil
}
