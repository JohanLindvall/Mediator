// Package dlna finds the UPnP/DLNA media renderers on the local network and
// tells them what to play.
//
// The direction is the opposite of AirPlay's and of the Remote Playback API's,
// which is exactly why it reaches sets those cannot. There, the browser holds
// the route and the page can only ask for a picker; a television found over
// DIAL never appears in one. Here the server calls the set directly over
// SOAP, hands it a URL on this machine's own LAN address, and the set fetches
// the file and decodes it itself. What plays is the file — its own quality,
// its own codecs, no conversion, and nothing held open here but a poll.
//
// Two consequences follow from the browser not being in the path. The control
// path runs through this server, so a film can be started on the television
// from a phone that is nowhere near either of them. And the set has to be
// able to reach *us*: the URL it is given is a LAN address, so this only
// works where the two are on the same network, which is the one thing the
// browser routes did not require.
package dlna

import (
	"bufio"
	"bytes"
	"context"
	"crypto/sha1"
	"encoding/hex"
	"encoding/xml"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/textproto"
	"net/url"
	"strings"
	"sync"
	"time"
)

const (
	ssdpAddr   = "239.255.255.250:1900"
	rendererST = "urn:schemas-upnp-org:device:MediaRenderer:1"

	// The three services a renderer offers. Only the first is required: the
	// others are what let us ask where it has got to and turn it down.
	avTransport   = "urn:schemas-upnp-org:service:AVTransport:1"
	connectionMgr = "urn:schemas-upnp-org:service:ConnectionManager:1"
	renderingCtl  = "urn:schemas-upnp-org:service:RenderingControl:1"

	describeTimeout = 4 * time.Second
	// A device description is a few kilobytes; anything claiming to be much
	// more is not one, and is not read.
	describeMax = 256 << 10
)

// Renderer is one device on the network that will play what it is given.
type Renderer struct {
	// ID is derived from the device's UDN rather than from its address, so a
	// television keeps the same identity across a restart of either end and a
	// client's chosen receiver survives a lease change.
	ID   string
	Name string
	UDN  string
	// Host is where the device answers, which is also how we work out which
	// of our own addresses it can reach us on.
	Host string

	control map[string]string // service type -> absolute control URL
	sinks   map[string]bool   // MIME types it says it accepts, empty when it would not say
}

// Discover asks the network for renderers and waits wait for the answers.
//
// The search goes out of every interface that could carry it rather than
// whichever one the default route happens to name: a machine that runs
// containers has a fistful of bridges, and a multicast sent out of one of
// those finds nothing at all.
func Discover(ctx context.Context, wait time.Duration) []*Renderer {
	locs := search(ctx, wait)

	var (
		mu    sync.Mutex
		found []*Renderer
		wg    sync.WaitGroup
	)
	// A noisy network answers with dozens of locations; describing them all
	// at once is dozens of fetches in flight for a list nobody needs faster.
	slots := make(chan struct{}, describeAtOnce)
	for loc := range locs {
		wg.Add(1)
		go func(loc string) {
			defer wg.Done()
			slots <- struct{}{}
			defer func() { <-slots }()
			r, err := describe(ctx, loc)
			if err != nil || r == nil {
				return
			}
			mu.Lock()
			defer mu.Unlock()
			for _, have := range found {
				if have.UDN == r.UDN { // answered on more than one interface
					return
				}
			}
			found = append(found, r)
		}(loc)
	}
	wg.Wait()
	return found
}

// search sends the M-SEARCH and collects the LOCATION of everything that
// answers. UDP loses datagrams and nobody retries them, so the request goes
// out twice per interface.
func search(ctx context.Context, wait time.Duration) map[string]bool {
	msg := []byte("M-SEARCH * HTTP/1.1\r\n" +
		"HOST: " + ssdpAddr + "\r\n" +
		"MAN: \"ssdp:discover\"\r\n" +
		"MX: 2\r\n" +
		"ST: " + rendererST + "\r\n\r\n")
	dst, err := net.ResolveUDPAddr("udp4", ssdpAddr)
	if err != nil {
		return nil
	}

	var (
		mu   sync.Mutex
		locs = map[string]bool{}
		wg   sync.WaitGroup
	)
	ifaces, err := net.Interfaces()
	if err != nil {
		return nil
	}
	for _, ifi := range ifaces {
		if ifi.Flags&net.FlagUp == 0 || ifi.Flags&net.FlagMulticast == 0 {
			continue
		}
		addrs, err := ifi.Addrs()
		if err != nil {
			continue
		}
		for _, a := range addrs {
			ipn, ok := a.(*net.IPNet)
			if !ok {
				continue
			}
			ip := ipn.IP.To4()
			if ip == nil || ip.IsLoopback() {
				continue
			}
			wg.Add(1)
			go func(ip net.IP) {
				defer wg.Done()
				conn, err := net.ListenUDP("udp4", &net.UDPAddr{IP: ip})
				if err != nil {
					return
				}
				defer conn.Close()
				deadline := time.Now().Add(wait)
				if d, ok := ctx.Deadline(); ok && d.Before(deadline) {
					deadline = d
				}
				_ = conn.SetDeadline(deadline)
				for range 2 {
					if _, err := conn.WriteToUDP(msg, dst); err != nil {
						return
					}
				}
				buf := make([]byte, 2048)
				for {
					n, _, err := conn.ReadFromUDP(buf)
					if err != nil {
						return
					}
					if loc := location(buf[:n]); loc != "" {
						mu.Lock()
						locs[loc] = true
						mu.Unlock()
					}
				}
			}(ip)
		}
	}
	wg.Wait()
	return locs
}

// location reads the LOCATION header out of an SSDP reply, which is an HTTP
// response with no body.
func location(b []byte) string {
	r := textproto.NewReader(bufio.NewReader(bytes.NewReader(b)))
	if _, err := r.ReadLine(); err != nil { // the status line
		return ""
	}
	h, err := r.ReadMIMEHeader()
	if err != nil {
		return ""
	}
	return h.Get("Location")
}

type xmlService struct {
	Type       string `xml:"serviceType"`
	ControlURL string `xml:"controlURL"`
}

type xmlDevice struct {
	FriendlyName string       `xml:"friendlyName"`
	UDN          string       `xml:"UDN"`
	Services     []xmlService `xml:"serviceList>service"`
	Devices      []xmlDevice  `xml:"deviceList>device"`
}

type xmlRoot struct {
	Device xmlDevice `xml:"device"`
}

// describe fetches a device description and turns it into a Renderer, or
// nothing where the device has no transport to drive.
func describe(ctx context.Context, loc string) (*Renderer, error) {
	base, err := url.Parse(loc)
	if err != nil {
		return nil, err
	}
	ctx, cancel := context.WithTimeout(ctx, describeTimeout)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, loc, nil)
	if err != nil {
		return nil, err
	}
	resp, err := client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("dlna: describe %s: %s", loc, resp.Status)
	}
	body, err := io.ReadAll(io.LimitReader(resp.Body, describeMax))
	if err != nil {
		return nil, err
	}
	var root xmlRoot
	if err := xml.Unmarshal(body, &root); err != nil {
		return nil, err
	}
	r := rendererFrom(root.Device, base)
	if r == nil {
		return nil, nil
	}
	r.Host = base.Host
	r.sinks = protocolSinks(ctx, r)
	return r, nil
}

// rendererFrom walks the description for the device that holds an
// AVTransport. Renderers commonly describe themselves at the top level, but
// the format allows a device to be a box containing others and some are.
func rendererFrom(d xmlDevice, base *url.URL) *Renderer {
	control := map[string]string{}
	for _, svc := range d.Services {
		if u, err := base.Parse(svc.ControlURL); err == nil {
			control[svc.Type] = u.String()
		}
	}
	if control[avTransport] != "" {
		sum := sha1.Sum([]byte(d.UDN))
		return &Renderer{
			ID:      hex.EncodeToString(sum[:])[:16],
			Name:    strings.TrimSpace(d.FriendlyName),
			UDN:     d.UDN,
			control: control,
		}
	}
	for _, sub := range d.Devices {
		if r := rendererFrom(sub, base); r != nil {
			return r
		}
	}
	return nil
}

// protocolSinks asks the device what it can play. A set that will not say is
// recorded as saying nothing, and Accepts then lets everything through: the
// device refusing on screen is a better failure than this server refusing on
// its behalf.
func protocolSinks(ctx context.Context, r *Renderer) map[string]bool {
	out, err := r.call(ctx, connectionMgr, "GetProtocolInfo")
	if err != nil {
		return nil
	}
	sinks := map[string]bool{}
	for _, entry := range strings.Split(out["Sink"], ",") {
		// http-get:*:video/x-matroska:DLNA.ORG_PN=...
		parts := strings.Split(entry, ":")
		if len(parts) < 3 {
			continue
		}
		if typ := strings.TrimSpace(parts[2]); typ != "" {
			sinks[strings.ToLower(typ)] = true
		}
	}
	if len(sinks) == 0 {
		return nil
	}
	return sinks
}

// Accepts reports whether the device listed this MIME type among the things
// it plays. An unknown answer is a yes; see protocolSinks.
func (r *Renderer) Accepts(mimeType string) bool {
	if len(r.sinks) == 0 {
		return true
	}
	return r.sinks[strings.ToLower(mimeType)]
}

// CanControlVolume reports whether the device exposes a volume to set.
func (r *Renderer) CanControlVolume() bool { return r.control[renderingCtl] != "" }

// LocalIPFor returns the address of ours that host can reach us on: the one
// the kernel would use to send it a packet. It is the mirror of the loopback
// address the library uses for its own reads — the receiver is not this
// machine, so 127.0.0.1 would name a server it cannot see.
func LocalIPFor(host string) string {
	h, _, err := net.SplitHostPort(host)
	if err != nil {
		h = host
	}
	c, err := net.Dial("udp4", net.JoinHostPort(h, "9"))
	if err != nil {
		return ""
	}
	defer c.Close()
	a, ok := c.LocalAddr().(*net.UDPAddr)
	if !ok || a.IP == nil {
		return ""
	}
	return a.IP.String()
}

// describeAtOnce bounds the fan-out of Discover's description fetches.
const describeAtOnce = 8

// client is the one HTTP client this package speaks to televisions with. Not
// http.DefaultClient: that keeps idle connections to every set ever polled
// and has no dial timeout of its own, and a set that has gone away should
// be given up on in seconds rather than at the operating system's leisure.
var client = &http.Client{
	Transport: &http.Transport{
		DialContext:         (&net.Dialer{Timeout: 5 * time.Second}).DialContext,
		IdleConnTimeout:     30 * time.Second,
		MaxIdleConnsPerHost: 2,
	},
}
