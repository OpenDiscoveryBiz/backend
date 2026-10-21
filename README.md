# OpenDiscovery backend

Go services for the root provider, dk-provider, resolver, and website. One module, four binaries, four scratch images.

```bash
cd web && npm ci && npm run build && cd ..
go test ./...
docker compose up --build
```

| Service | Port | Image |
| --- | --- | --- |
| root-provider | 18081 | ghcr.io/opendiscoverybiz/root-provider |
| dk-provider | 18082 | ghcr.io/opendiscoverybiz/dk-provider |
| resolver | 18083 | ghcr.io/opendiscoverybiz/resolver |
| website | 18084 | ghcr.io/opendiscoverybiz/website |

`dk-provider` reads `ERST_CVR_USER` and `ERST_CVR_PASS`. Empty credentials still boot; CVR lookups return `upstream_down`. Other env vars from the old Laravel apps are ignored.
