---
kind: improvement
area: api
---
The permissions for ideal customer profiles and moving records between brands are now called `profiles.read` and `profiles.manage`, and the API reference lists those endpoints under "profiles" instead of "projects". The old names `projects.read` and `projects.manage` still work: tokens, roles and apps that hold them keep their access, and requests may still send them.
