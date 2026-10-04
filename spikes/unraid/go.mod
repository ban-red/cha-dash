// Throwaway spike, kept out of the main dash.cha.sh module. It reuses the labkit package
// (sanitizer, leak check) from the Traefik spike, like spikes/docker-proxy.
module dash.cha.sh/spikes/unraid

go 1.27.1

require (
	dash.cha.sh/spikes/traefik v0.0.0
	github.com/coder/websocket v1.8.15
	github.com/vektah/gqlparser/v2 v2.5.60
)

replace dash.cha.sh/spikes/traefik => ../traefik
