# Slate Viewer's website build

Metatrash serves Slate Viewer's UI at /app/ (0.73.0) from the files in this
folder. They come from the Slate Viewer repository:

    npm run build:web

then replace everything here except this README with the contents of its
dist-web folder (assets/ and .vite/manifest.json). Without them, /app/
pages answer that the new viewer is not in this build.
