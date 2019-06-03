# IOTA Caddy

This version of Caddy has an IOTA interceptor middleware which intercepts calls to an IRI node by parsing
the JSON command and then executing `attachToTangle` within the middleware, instead of delegating it to IRI.
Other IRI API commands are delegated to the specified IRI node.

A `Caddyfile` to use with this Caddy version can look like:
```
127.0.0.1:15265 {

        gzip

        # log requests to the proxy with rotation
        log requests.log {
                rotate_size 100
                rotate_age  90
                rotate_keep 20
                rotate_compress
        }

        #tls /etc/letsencrypt/live/iota-tangle.io/fullchain.pem /etc/letsencrypt/live/iota-tangle.io/privkey.pem

        # limit request body to 10 megabytes
        limits 10mb

        proxy / http://127.0.0.1:14265 {
                header_upstream X-IOTA-API-VERSION 1.4
                header_upstream Access-Control-Allow-Origin *
        }

        # intercept attachToTangle calls with a max MWM of 14 and 20 txs per call
        iota 14 20
}
```

where the `iota` directive instructs Caddy to execute the middleware. The first argument defines the maximum
allowed minimum weight magnitude within the request and the second the maximum amount of transactions to commence
Proof of Work for.

# Build/Install

Prerequisites:
* Linux system
* GCC compiler
* At least Go version 1.12
* Git

1. Pull the repository from GitHub into a directory outside of $GOPATH:  
`git clone git@github.com:luca-moser/iotacaddy.git`
2. `cd iotacaddy/caddy`
3. compile Caddy with the AVX or SSE implementation: `go build -tags="pow_avx"` or `go build -tags="pow_sse"`
4. This will create a binary called `caddy` in the `iotacaddy/caddy` folder.

# Run
1. Move the `caddy` binary and the corresponding `Caddyfile` into the same directory of your choice  
2. Adjust the directives, hostname and IRI URL
3. Run caddy via `./caddy` which will the configured interceptor parameters up on startup:

```
Activating privacy features... done.                                                                                             
[iota interceptor] 2019/06/03 12:56:54 iota API call interception configured with max bundle txs limit of 20 and max MWM of 14   
[iota interceptor] 2019/06/03 12:56:54 using PoW implementation: SyncAVX                                                         
                                                                                                                                 
Serving HTTPS on port 14265                                                                                                      
https://trinity.iota-tangle.io:14265
```

If an `attachToTangle` calls get intercepted, the middleware will log it in stdout similar to:
```
[iota interceptor] 2019/06/03 12:58:08 new attachToTangle request from 80.218.171.223:33442                                      │
[iota interceptor] 2019/06/03 12:58:08 VBDKIEDKVYHWGROOMBEFZOJFAVITMGQBASCPVZWYTVRFSLMJTNYOOZVGVBEFZINUTVQI9VZQCISPDXJN9 - [input│
] -0.000001 Mi                                                                                                                   │
[iota interceptor] 2019/06/03 12:58:08 bundle: PYPWHTVICKOEJ9CFHDHOJJAWDARUZNPOIXONCYKHLFEFEGKXWSEJXGZSXKTS9BBGNKTQGQZCLCHPLIKIC │
[iota interceptor] 2019/06/03 12:58:08 bundle is using -0.000001 Mi as input                                                     │
[iota interceptor] 2019/06/03 12:58:08 doing PoW for bundle with 1 txs...                                                        │
[iota interceptor] 2019/06/03 12:58:08 took 273ms to do PoW for bundle with 1 txs
```

Caddy will generate a `requests.log` file containing the requests against the proxy.