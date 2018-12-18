# cloudflare-roundtripper

Working Go implementation inspired by Anorov/cloudflare-scrape written in python


## Example

```go
import(
    "time"
    "net"
    "net/http"

    "github.com/caffix/cloudflare-roundtripper/cfrt"
)

func main() {
    var err error

    // Setup your client however you need it. This is simply an example
    client := &http.Client{
        Timeout: 15 * time.Second,
        Transport: &http.Transport{
            DialContext: (&net.Dialer{
                Timeout:   15 * time.Second,
                KeepAlive: 15 * time.Second,
                DualStack: true,
            }).DialContext,
        },
    }
    // Set the client Transport to the RoundTripper that solves the Cloudflare anti-bot
    client.Transport, err = cfrt.New(client.Transport)
    if err != nil {
        return
    }
    
    req, err := http.NewRequest("GET", "http://example.com/path", nil)
    if err != nil {
        return
    }
    
    resp, err := client.Do(req)
    if err != nil {
        return
    }
    // Use the response as you wish...
}
```
