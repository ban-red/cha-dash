---
layout: home

hero:
  name: Cha Dash
  text: A calm, live page for your homelab
  tagline: One page that shows what's happening and can act on it, with a plan, the confirmation each action's risk needs, and an audit log. Self-hosted, one container, AGPL.
  actions:
    - theme: brand
      text: Install
      link: /install
    - theme: alt
      text: Integrations
      link: /integrations/
    - theme: alt
      text: GitHub
      link: https://github.com/ban-red/cha-dash

features:
  - title: Install in three minutes
    details: Docker Compose, no settings required. Unraid has a template, and every release has signed binaries.
    link: /install
  - title: Reads what you run
    details: Proxmox, Unraid, Docker, the *arrs, Home Assistant and more, each with a least-privilege credential recipe.
    link: /integrations/
  - title: Acts with care
    details: Every action is planned, authorized, confirmed by its risk and audited. The hub never holds Docker write access; nodes do, within each host's policy.
    link: /security
  - title: Open about its risks
    details: The threat model names what Cha Dash guards, the boundaries it trusts, and the gaps it still has.
    link: /threat-model
  - title: An API for scripts
    details: Every route, generated from the hub's route table, with the role and token scope each one needs.
    link: /api/
  - title: Your own integrations
    details: A service that isn't built in can be added as a YAML spec, checked and tested like the bundled ones.
    link: /specs
---
