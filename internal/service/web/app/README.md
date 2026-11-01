# Slate Viewer's website build

Metatrash serves Slate Viewer's UI at a space's own addresses (/spaces/...
and /docs/..., since 0.87.0; /app/ from 0.73.0) from the files in this folder.
The hashed files are served from /app/assets/. They come from the Slate Viewer
repository's website build. Run, from the Metatrash checkout (with Slate and Slate_Viewer next to it):

    powershell -ExecutionPolicy Bypass -File scripts\build-frontends.ps1

It builds Slate, copies it into Slate Viewer and into ../ (slate.js,
slate.css, mermaid/), runs Slate Viewer's `npm run build:web`, replaces
assets/ and .vite/ here with its dist-web folder, and records both versions
and commits in ../frontends.json. Without these files, space pages answer
that the site app is not in this build.
