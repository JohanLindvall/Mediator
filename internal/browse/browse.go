// Package browse opens a URL in the user's default browser.
package browse

import (
	"fmt"
	"net"
	"os/exec"
	"runtime"
	"strconv"
	"strings"
)

// URL builds the address to visit for a server configured with listenAddr
// and actually bound to port. A wildcard or empty host becomes loopback:
// "http://0.0.0.0:8080" is not something a browser can usefully open.
func URL(listenAddr string, port int) string {
	host, _, err := net.SplitHostPort(listenAddr)
	if err != nil {
		host = ""
	}
	switch host {
	case "", "0.0.0.0", "::":
		host = "127.0.0.1"
	}
	return "http://" + net.JoinHostPort(host, strconv.Itoa(port)) + "/"
}

// opener is a helper program plus the arguments preceding the URL.
type opener struct {
	name string
	args []string
}

// openers lists the URL handlers to try, in order, for this platform.
func openers() []opener {
	switch runtime.GOOS {
	case "darwin":
		return []opener{{name: "open"}}
	case "windows":
		return []opener{{name: "rundll32", args: []string{"url.dll,FileProtocolHandler"}}}
	default:
		return []opener{
			{name: "xdg-open"},
			{name: "gio", args: []string{"open"}},
			{name: "gnome-open"},
			{name: "kde-open"},
			{name: "wslview"}, // WSL: hands off to the Windows browser
			{name: "x-www-browser"},
		}
	}
}

// Open launches the first available URL handler on the URL. It returns as
// soon as the helper has started — some of them stay alive for as long as
// the browser does.
func Open(url string) error {
	var tried []string
	for _, o := range openers() {
		path, err := exec.LookPath(o.name)
		if err != nil {
			tried = append(tried, o.name)
			continue
		}
		cmd := exec.Command(path, append(append([]string{}, o.args...), url)...)
		if err := cmd.Start(); err != nil {
			tried = append(tried, o.name)
			continue
		}
		go func() { _ = cmd.Wait() }() // reap when it exits
		return nil
	}
	return fmt.Errorf("no usable URL opener (tried %s)", strings.Join(tried, ", "))
}
