//go:build ignore

// load drives fugace for docs/BENCH.md (run through run.sh).
//
//	go run load.go -mode chat -irc fugace:6667 -locals 172.31.77.100-159 -clients 500 -rate 50 -duration 60s
//	go run load.go -mode flood -irc fugace:6667 -locals 172.31.77.100-109
//	go run load.go -mode slowloris -https fugace:8443 -irc fugace:6667 -locals 172.31.77.100-101
package main

import (
	"bufio"
	"crypto/tls"
	"flag"
	"fmt"
	"net"
	"net/http"
	"os"
	"slices"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"
)

func main() {
	mode := flag.String("mode", "chat", "chat | flood | slowloris")
	ircAddr := flag.String("irc", "fugace:6667", "IRC address")
	httpsAddr := flag.String("https", "fugace:8443", "HTTPS address")
	locals := flag.String("locals", "", "local source IPs, a.b.c.FIRST-LAST")
	clients := flag.Int("clients", 500, "chat clients")
	rate := flag.Int("rate", 50, "messages per second, all clients together")
	duration := flag.Duration("duration", 60*time.Second, "measurement duration")
	flag.Parse()
	srcs, err := parseLocals(*locals)
	if err != nil {
		fail(err)
	}
	switch *mode {
	case "chat":
		chat(*ircAddr, srcs, *clients, *rate, *duration)
	case "flood":
		flood(*ircAddr, srcs)
	case "slowloris":
		slowloris(*httpsAddr, *ircAddr, srcs)
	default:
		fail(fmt.Errorf("unknown mode %q", *mode))
	}
}

func fail(err error) {
	fmt.Println("FAIL:", err)
	os.Exit(1)
}

func parseLocals(s string) ([]net.IP, error) {
	if s == "" {
		return []net.IP{nil}, nil
	}
	base, rng, ok := strings.Cut(s, "-")
	i := strings.LastIndexByte(base, '.')
	if !ok || i < 0 {
		return nil, fmt.Errorf("bad -locals %q", s)
	}
	first, err1 := strconv.Atoi(base[i+1:])
	last, err2 := strconv.Atoi(rng)
	if err1 != nil || err2 != nil || last < first {
		return nil, fmt.Errorf("bad -locals %q", s)
	}
	var out []net.IP
	for n := first; n <= last; n++ {
		out = append(out, net.ParseIP(base[:i+1]+strconv.Itoa(n)))
	}
	return out, nil
}

func dial(addr string, src net.IP) (net.Conn, error) {
	d := net.Dialer{Timeout: 5 * time.Second}
	if src != nil {
		d.LocalAddr = &net.TCPAddr{IP: src}
	}
	return d.Dial("tcp", addr)
}

type client struct {
	conn net.Conn
	w    *bufio.Writer
	mu   sync.Mutex
}

func (c *client) send(lines ...string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	for _, l := range lines {
		c.w.WriteString(l + "\r\n")
	}
	c.w.Flush()
}

// connect registers nick and joins #general; onLine sees every later line.
func connect(addr string, src net.IP, nick string, onLine func(string)) (*client, error) {
	conn, err := dial(addr, src)
	if err != nil {
		return nil, err
	}
	c := &client{conn: conn, w: bufio.NewWriter(conn)}
	r := bufio.NewReaderSize(conn, 8192)
	c.send("NICK "+nick, "USER l 0 * :l", "JOIN #general")
	_ = conn.SetReadDeadline(time.Now().Add(30 * time.Second))
	for {
		l, err := r.ReadString('\n')
		if err != nil {
			return nil, fmt.Errorf("%s: %w", nick, err)
		}
		if strings.Contains(l, " 366 "+nick+" #general ") {
			break
		}
	}
	_ = conn.SetReadDeadline(time.Time{})
	go func() {
		for {
			l, err := r.ReadString('\n')
			if err != nil {
				return
			}
			if strings.HasPrefix(l, "PING ") {
				c.send("PONG " + strings.TrimSpace(l[5:]))
				continue
			}
			onLine(l)
		}
	}()
	return c, nil
}

type stats struct {
	mu   sync.Mutex
	lats []time.Duration
	recv atomic.Int64
}

func (s *stats) record(line string) {
	i := strings.Index(line, " :t=")
	if i < 0 {
		return
	}
	ns, err := strconv.ParseInt(strings.TrimSpace(line[i+4:]), 10, 64)
	if err != nil {
		return
	}
	d := time.Since(time.Unix(0, ns))
	s.recv.Add(1)
	s.mu.Lock()
	s.lats = append(s.lats, d)
	s.mu.Unlock()
}

func pct(l []time.Duration, p float64) time.Duration {
	if len(l) == 0 {
		return 0
	}
	return l[min(int(float64(len(l))*p), len(l)-1)]
}

func chat(addr string, srcs []net.IP, n, rate int, dur time.Duration) {
	st := &stats{}
	cs := make([]*client, 0, n)
	start := time.Now()
	for i := range n {
		c, err := connect(addr, srcs[i%len(srcs)], fmt.Sprintf("load%03d", i), st.record)
		if err != nil {
			fail(err)
		}
		cs = append(cs, c)
	}
	fmt.Printf("connected %d clients from %d sources in %v\n", n, len(srcs), time.Since(start).Round(time.Millisecond))
	time.Sleep(2 * time.Second) // let join bursts settle
	tick := time.NewTicker(time.Second / time.Duration(rate))
	defer tick.Stop()
	end := time.Now().Add(dur)
	sent := 0
	for time.Now().Before(end) {
		<-tick.C
		cs[sent%n].send("PRIVMSG #general :t=" + strconv.FormatInt(time.Now().UnixNano(), 10))
		sent++
	}
	time.Sleep(3 * time.Second) // drain
	st.mu.Lock()
	lats := slices.Clone(st.lats)
	st.mu.Unlock()
	slices.Sort(lats)
	want := int64(sent) * int64(n-1)
	got := st.recv.Load()
	fmt.Printf("sent %d messages (%d/s) to %d members: %d deliveries expected, %d received (%.3f%% lost)\n",
		sent, rate, n, want, got, 100*float64(want-got)/float64(want))
	fmt.Printf("latency p50 %v  p95 %v  p99 %v  max %v\n", pct(lats, .50).Round(time.Microsecond),
		pct(lats, .95).Round(time.Microsecond), pct(lats, .99).Round(time.Microsecond), pct(lats, 1).Round(time.Microsecond))
	if got < want {
		fail(fmt.Errorf("%d deliveries lost", want-got))
	}
}

// flood: one client sends as fast as it can; the server must fake-lag it
// (2/s after a burst of 10) while a normal client's latency stays low.
func flood(addr string, srcs []net.IP) {
	normal := &stats{}
	var fmu sync.Mutex
	var floodTimes []time.Time
	watcher, err := connect(addr, srcs[0], "watcher", func(l string) {
		if strings.HasPrefix(l, ":flooder!") && strings.Contains(l, " :flood ") {
			fmu.Lock()
			floodTimes = append(floodTimes, time.Now())
			fmu.Unlock()
		}
		normal.record(l)
	})
	if err != nil {
		fail(err)
	}
	_ = watcher
	talker, err := connect(addr, srcs[len(srcs)-1], "talker", func(string) {})
	if err != nil {
		fail(err)
	}
	flooder, err := connect(addr, srcs[len(srcs)/2], "flooder", func(string) {})
	if err != nil {
		fail(err)
	}
	go func() {
		lines := make([]string, 2000)
		for i := range lines {
			lines[i] = "PRIVMSG #general :flood " + strconv.Itoa(i)
		}
		flooder.send(lines...) // 2000 lines at once
	}()
	// The talker stays under its own limit (2/s) so that only the flood
	// could explain a delay.
	const window = 10 * time.Second
	const every = 600 * time.Millisecond
	for range int(window / every) {
		talker.send("PRIVMSG #general :t=" + strconv.FormatInt(time.Now().UnixNano(), 10))
		time.Sleep(every)
	}
	time.Sleep(time.Second)
	normal.mu.Lock()
	lats := slices.Clone(normal.lats)
	normal.mu.Unlock()
	slices.Sort(lats)
	fmu.Lock()
	ft := slices.Clone(floodTimes)
	fmu.Unlock()
	const burst = 10
	if len(ft) <= burst+1 {
		fail(fmt.Errorf("flooder silenced entirely: %d lines", len(ft)))
	}
	span := ft[len(ft)-1].Sub(ft[burst])
	rate := float64(len(ft)-1-burst) / span.Seconds()
	fmt.Printf("flood: 2000 lines sent at once, %d relayed; after the burst of %d: %.2f lines/s (limit 2/s)\n", len(ft), burst, rate)
	fmt.Printf("flood: other client's latency meanwhile p50 %v  max %v (%d messages)\n",
		pct(lats, .5).Round(time.Microsecond), pct(lats, 1).Round(time.Microsecond), len(lats))
	if rate > 2.2 {
		fail(fmt.Errorf("flooder not rate limited: %.2f lines/s", rate))
	}
	if pct(lats, 1) > time.Second {
		fail(fmt.Errorf("flood degraded other clients: max latency %v", pct(lats, 1)))
	}
}

// slowloris: idle connections from one source are capped and timed out,
// and the service keeps answering everybody else.
func slowloris(httpsAddr, ircAddr string, srcs []net.IP) {
	attacker, bystander := srcs[0], srcs[len(srcs)-1]
	const n = 200
	var open []net.Conn
	refused := 0
	for range n {
		c, err := dial(httpsAddr, attacker)
		if err != nil {
			refused++
			continue
		}
		_, _ = c.Write([]byte{0x16, 0x03, 0x01}) // start of a TLS record, then nothing
		open = append(open, c)
	}
	time.Sleep(300 * time.Millisecond)
	alive := 0
	for _, c := range open {
		_ = c.SetReadDeadline(time.Now().Add(10 * time.Millisecond))
		if _, err := c.Read(make([]byte, 1)); err != nil {
			if ne, ok := err.(net.Error); ok && ne.Timeout() {
				alive++
			}
		}
	}
	fmt.Printf("slowloris https: %d connections from one source, %d kept open by the server (cap 32)\n", n, alive)
	if alive > 32 {
		fail(fmt.Errorf("per-source cap not enforced: %d", alive))
	}
	// Meanwhile another source is served normally.
	tr := &http.Transport{
		TLSClientConfig: &tls.Config{InsecureSkipVerify: true},
		DialContext:     (&net.Dialer{LocalAddr: &net.TCPAddr{IP: bystander}}).DialContext,
	}
	t0 := time.Now()
	resp, err := (&http.Client{Transport: tr, Timeout: 5 * time.Second}).Get("https://" + httpsAddr + "/healthz")
	if err != nil {
		fail(fmt.Errorf("bystander not served: %w", err))
	}
	resp.Body.Close()
	fmt.Printf("slowloris https: another source served in %v (status %d)\n", time.Since(t0).Round(time.Millisecond), resp.StatusCode)
	// The idle ones are dropped by the handshake/header timeout (5 s).
	t0 = time.Now()
	for _, c := range open {
		_ = c.SetReadDeadline(time.Now().Add(15 * time.Second))
		_, _ = c.Read(make([]byte, 1))
		c.Close()
	}
	fmt.Printf("slowloris https: all idle connections closed by the server after %v\n", time.Since(t0).Round(100*time.Millisecond))
	if time.Since(t0) > 8*time.Second {
		fail(fmt.Errorf("idle connections kept too long"))
	}

	// IRC: unregistered connections, per-source limit 10, then 30 s timeout.
	var irc []net.Conn
	for range 30 {
		c, err := dial(ircAddr, attacker)
		if err == nil {
			irc = append(irc, c)
		}
	}
	time.Sleep(300 * time.Millisecond)
	kept, tooMany := 0, 0
	for _, c := range irc {
		_ = c.SetReadDeadline(time.Now().Add(50 * time.Millisecond))
		l, err := bufio.NewReader(c).ReadString('\n')
		switch {
		case strings.Contains(l, "Too many connections"):
			tooMany++
		case err != nil:
			if ne, ok := err.(net.Error); ok && ne.Timeout() {
				kept++
			}
		}
	}
	fmt.Printf("slowloris irc: 30 idle connections from one source: %d kept, %d refused with 'Too many connections'\n", kept, tooMany)
	if kept > 10 {
		fail(fmt.Errorf("IRC per-source limit not enforced: %d", kept))
	}
	t0 = time.Now()
	for _, c := range irc {
		_ = c.SetReadDeadline(time.Now().Add(40 * time.Second))
		r := bufio.NewReader(c)
		for {
			if _, err := r.ReadString('\n'); err != nil {
				break
			}
		}
		c.Close()
	}
	fmt.Printf("slowloris irc: unregistered connections closed after %v (registration timeout 30 s)\n", time.Since(t0).Round(time.Second))
}
