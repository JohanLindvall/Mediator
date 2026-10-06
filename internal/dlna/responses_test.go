// SPDX-License-Identifier: MIT

package dlna

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestDescribeResolvesControlURLs(t *testing.T) {
	for _, useBase := range []bool{false, true} {
		t.Run(fmt.Sprintf("URLBase=%v", useBase), func(t *testing.T) {
			var base string
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.URL.Path == "/old.xml" {
					http.Redirect(w, r, "/device/desc.xml", http.StatusFound)
					return
				}
				if r.Method == http.MethodPost {
					want := "/device/control"
					if useBase {
						want = "/services/control"
					}
					if r.URL.Path != want {
						http.NotFound(w, r)
						return
					}
					fmt.Fprint(w, `<s:Envelope xmlns:s="http://schemas.xmlsoap.org/soap/envelope/"><s:Body><PlayResponse/></s:Body></s:Envelope>`)
					return
				}
				urlBase := ""
				if useBase {
					urlBase = "<URLBase>" + base + "/services/</URLBase>"
				}
				fmt.Fprintf(w, `<root>%s<device><friendlyName>A Screen</friendlyName><UDN>uuid:screen</UDN><serviceList><service><serviceType>%s</serviceType><controlURL>control</controlURL></service></serviceList></device></root>`, urlBase, avTransport)
			}))
			defer srv.Close()
			base = srv.URL
			r, err := Describe(context.Background(), srv.URL+"/old.xml")
			if err != nil || r == nil {
				t.Fatalf("Describe = %v, %v", r, err)
			}
			if err := r.Play(context.Background()); err != nil {
				t.Fatalf("relative control URL did not resolve: %v", err)
			}
		})
	}
}

func TestSOAPRejectsInvalidResponses(t *testing.T) {
	const start = `<s:Envelope xmlns:s="http://schemas.xmlsoap.org/soap/envelope/"><s:Body>`
	const end = `</s:Body></s:Envelope>`
	for name, body := range map[string]string{
		"empty":                 "",
		"truncated":             start + `<PlayResponse>`,
		"HTML":                  `<html><body>Device unavailable</body></html>`,
		"too large":             start + `<PlayResponse/>` + end + strings.Repeat(" ", describeMax),
		"fault with status 200": start + `<s:Fault><faultcode>s:Client</faultcode><detail><UPnPError><errorCode>701</errorCode><errorDescription>Unavailable</errorDescription></UPnPError></detail></s:Fault>` + end,
	} {
		t.Run(name, func(t *testing.T) {
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				fmt.Fprint(w, body)
			}))
			defer srv.Close()
			r := &Renderer{Name: "A Screen", control: map[string]string{avTransport: srv.URL}}
			if err := r.Play(context.Background()); err == nil {
				t.Fatal("invalid SOAP response reported success")
			}
		})
	}
}
