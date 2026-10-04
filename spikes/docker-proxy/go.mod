// Throwaway spike, kept out of the main dash.cha.sh module. It reuses the
// labkit package (env, read-only client, sanitizer, leak check) from the
// Traefik spike.
module dash.cha.sh/spikes/docker-proxy

go 1.27.1

require dash.cha.sh/spikes/traefik v0.0.0

replace dash.cha.sh/spikes/traefik => ../traefik
