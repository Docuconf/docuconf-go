# docuconf website

The project site, built with [Astro Starlight](https://starlight.astro.build). Pages are in `src/content/docs`.

```
npm ci
npm run dev      # http://localhost:4321
npm run build    # static output in dist/
```

`.github/workflows/website.yml` deploys it to GitHub Pages on every push to `main`.
