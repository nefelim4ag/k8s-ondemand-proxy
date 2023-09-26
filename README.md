# k8s-ondemand-proxy
Simple TCP proxy app that will scale from zero Deployment/StatefulSet  in case of external requests and scale down when no traffic

# How it works
Very similar to Knative but dead simple
```
Ingress -> ondemand-proxy -> service -> pod
# OR
App -> ondemand-proxy -> service -> pod
```

When a connection comes to ondemand-proxy, it holds it up, while in the background starts to scale the target app.
When the target app is ready, traffic will be proxied.
When no traffic comes to the proxy in an idle period, 15 minutes by default, the proxied app will be scaled to 0.

Example
[![asciicast](https://asciinema.org/a/ZlIbWm8UQv4yVaOqfm3IzUPoT.svg)](https://asciinema.org/a/ZlIbWm8UQv4yVaOqfm3IzUPoT)
