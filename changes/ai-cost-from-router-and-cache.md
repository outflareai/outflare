---
kind: fix
area: workspace
---
An extension's AI calls now report what they cost on a router such as OpenRouter (not 0), with cached input at its cheaper rate. Through OpenRouter, Anthropic's models now cache the stable start of a request, as a direct Claude connection already did. Extensions can list the models a workspace's AI connection offers.
