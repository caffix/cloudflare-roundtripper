// Copyright 2018 Jeff Foley. All rights reserved.
// Use of this source code is governed by Apache 2 LICENSE that can be found in the LICENSE file.

package cfrt

import (
	"errors"
	"fmt"
	"io/ioutil"
	"net/http"
	"net/http/cookiejar"
	"net/url"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/robertkrimen/otto"
)

const (
	// UserAgent is the default user agent used by HTTP requests.
	UserAgent = "Mozilla/5.0 (Windows NT 10.0; WOW64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/65.0.3325.181 Safari/537.36"
)

var (
	jschlRE = regexp.MustCompile(`name="jschl_vc" value="(\w+)"`)
	passRE  = regexp.MustCompile(`name="pass" value="(.+?)"`)
	jsRE    = regexp.MustCompile(
		`setTimeout\(function\(\){\s+(var ` +
			`s,t,o,p,b,r,e,a,k,i,n,g,f.+?\r?\n[\s\S]+?a\.value =.+?)\r?\n`,
	)
	jsReplace1RE = regexp.MustCompile(`a\.value = (.+ \+ t\.length).+`)
	jsReplace2RE = regexp.MustCompile(`\s{3,}[a-z](?: = |\.).+`)
	jsReplace3RE = regexp.MustCompile(`[\n\\']`)
)

// RoundTripper is a http client RoundTripper that can handle the Cloudflare anti-bot.
type RoundTripper struct {
	upstream http.RoundTripper
	cookies  http.CookieJar
}

// New wraps a http client transport with one that can handle the Cloudflare anti-bot.
func New(upstream http.RoundTripper) (*RoundTripper, error) {
	jar, err := cookiejar.New(nil)
	if err != nil {
		return nil, err
	}
	return &RoundTripper{upstream, jar}, nil
}

// RoundTrip implements the RoundTripper interface for the Transport type.
func (rt RoundTripper) RoundTrip(r *http.Request) (*http.Response, error) {
	if r.Header.Get("User-Agent") == "" {
		r.Header.Set("User-Agent", UserAgent)
	}
	// Pass along Cloudflare cookies obtained previously
	for _, cookie := range rt.cookies.Cookies(r.URL) {
		r.AddCookie(cookie)
	}

	resp, err := rt.upstream.RoundTrip(r)
	if err != nil {
		return nil, err
	}
	// Check if the Cloudflare anti-bot has prevented the request
	if resp.StatusCode == 503 && strings.HasPrefix(resp.Header.Get("Server"), "cloudflare") {
		req, err := buildAnswerRequest(resp)
		if err != nil {
			return nil, err
		}
		// Cloudflare requires a delay before solving the challenge
		time.Sleep(5 * time.Second)
		resp, err = rt.upstream.RoundTrip(req)
		if err != nil {
			return nil, err
		}
		// Save the cookies obtained from the Cloudflare challenge
		if cookies := resp.Cookies(); len(cookies) > 0 {
			rt.cookies.SetCookies(resp.Request.URL, resp.Cookies())
		}
	}
	return resp, err
}

func buildAnswerRequest(resp *http.Response) (*http.Request, error) {
	b, err := ioutil.ReadAll(resp.Body)
	resp.Body.Close()
	if err != nil {
		return nil, err
	}

	js, err := extractJS(string(b), resp.Request.URL.Host)
	if err != nil {
		return nil, err
	}
	// Obtain the answer from the JavaScript challenge
	num, err := evaluateJS(js)
	answer := fmt.Sprintf("%.10f", num)
	if err != nil {
		return nil, err
	}
	// Begin building the URL for submitting the answer
	chkURL, _ := url.Parse("/cdn-cgi/l/chk_jschl")
	u := resp.Request.URL.ResolveReference(chkURL)
	// Obtain all the parameters for the URL
	var params = make(url.Values)
	if m := jschlRE.FindStringSubmatch(string(b)); len(m) > 0 {
		params.Set("jschl_vc", m[1])
	}
	if m := passRE.FindStringSubmatch(string(b)); len(m) > 0 {
		params.Set("pass", m[1])
	}
	params.Set("jschl_answer", answer)
	u.RawQuery = params.Encode()

	req, err := http.NewRequest("GET", u.String(), nil)
	if err != nil {
		return nil, err
	}
	// Copy all the header values from the original request
	if resp.Request.Header != nil {
		for key, vals := range resp.Request.Header {
			for _, val := range vals {
				req.Header.Add(key, val)
			}
		}
	}
	req.Header.Set("Referer", resp.Request.URL.String())
	return req, nil
}

func extractJS(body, domain string) (string, error) {
	matches := jsRE.FindStringSubmatch(body)
	if len(matches) == 0 {
		return "", errors.New("Unable to identify Cloudflare IUAM Javascript on the page")
	}

	js := matches[1]
	js = jsReplace1RE.ReplaceAllString(js, "$1")
	js = jsReplace2RE.ReplaceAllString(js, "")
	js = strings.Replace(js, "t.length", strconv.Itoa(len(domain)), -1)
	// Strip characters that could be used to exit the string context
	// These characters are not currently used in Cloudflare's arithmetic snippet
	js = jsReplace3RE.ReplaceAllString(js, "")
	return js, nil
}

type ottoReturn struct {
	Result float64
	Err    error
}

var errHalt = errors.New("Stop")

func evaluateJS(js string) (float64, error) {
	var err error
	var result float64
	interrupt := make(chan func())
	ret := make(chan *ottoReturn)
	t := time.NewTimer(5 * time.Second)
	defer t.Stop()

	go executeUnsafeJS(js, interrupt, ret)
loop:
	for {
		select {
		case <-t.C:
			interrupt <- func() {
				panic(errHalt)
			}
		case r := <-ret:
			result = r.Result
			err = r.Err
			break loop
		}
	}
	return result, err
}

func executeUnsafeJS(js string, interrupt chan func(), ret chan *ottoReturn) {
	var num float64

	vm := otto.New()
	vm.Interrupt = interrupt

	defer func() {
		if caught := recover(); caught != nil {
			if caught == errHalt {
				ret <- &ottoReturn{
					Result: num,
					Err:    errors.New("The unsafe Javascript ran for too long"),
				}
				return
			}
			panic(caught)
		}
	}()

	result, err := vm.Run(js)
	if err == nil {
		num, err = result.ToFloat()
	}
	ret <- &ottoReturn{
		Result: num,
		Err:    err,
	}
}
