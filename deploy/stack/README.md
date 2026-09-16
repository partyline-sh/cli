# The partyline stack — images, pulling, and pinning

`docker-compose.yml` in this directory is the whole product: Postgres, PostgREST, Keycloak, Redis,
the web app, the relay, a ticker, MinIO and Caddy. There is one stack and one file — no separate
"self-host edition" that could quietly rot, because self-hosting is the only way the server runs.
There is no hosted partyline; `partyline.sh` serves documentation.

`ptln server install` writes these files onto a box, fills in `.env`, and brings the stack up. Doing
it by hand, as below, is the same thing done deliberately.

Two of those services are images we publish. **Both are public packages on GHCR: no `docker login`,
no personal access token, no GitHub account.**

| Service | Image | Contents |
|---|---|---|
| `web` | `ghcr.io/partyline-sh/partyline-web` | the Next.js control plane |
| `relay` | `ghcr.io/partyline-sh/partyline-relay` | the blind E2EE session relay |

Everything else in the stack is a stock upstream image (`postgres:16`,
`postgrest/postgrest:v12.2.3`, `redis:7-alpine`, `caddy:2-alpine`, `busybox:1.36`) pulled from its
own public registry.

## Pull them

The GHCR packages are private, so an anonymous pull of these returns `unauthorized` — this is not
the check to hand a stranger:

```sh
docker pull ghcr.io/partyline-sh/partyline-web:latest   # requires credentials
```

The published images live on Docker Hub, and those are the ones anyone can pull with no account:

```sh
docker pull partyline/partyline-web:latest
docker pull partyline/partyline-relay:latest
```

They are published as multi-arch manifest lists — `linux/amd64` and `linux/arm64` — so Docker
resolves the right one for you. Apple Silicon, Graviton, Ampere and Hetzner's ARM instances run the
native arm64 image, not an emulated one, and the published compose no longer pins a platform.

(It used to. Until the buildx change now carried by `.github/workflows/build-images.yml` these were amd64
only, and an ARM pull failed with `no matching manifest for linux/arm64/v8`. If you are on a pinned
older tag from before that, `--platform linux/amd64` still gets you the emulated image.)

To prove no credential is involved — rather than trusting that your Docker config is empty — point
Docker at a throwaway config directory. This is also what CI does, because `docker logout` would
log the *machine* out as a side effect of running a test:

```sh
docker --config "$(mktemp -d)" pull ghcr.io/partyline-sh/partyline-web:latest
```

## Pin a version — do this

`:latest` is whatever the maintainers most recently promoted. It **moves**, on their schedule and
without telling you: the next time anything on your box pulls, you may get different software. Fine
for a first look; not what you want running a team's data.

Three tags exist, in increasing order of how much they promise:

| Tag | Means | Moves? |
|---|---|---|
| `:latest` | the newest promoted build | yes, on every promotion |
| `:prod-<sha>` | the build promoted from the commit `<sha>` on `main` | no |
| `@sha256:<digest>` | those exact bytes | never, by construction |

**Pin by digest.** A tag is a pointer someone can repoint; a digest *is* the content, so
`@sha256:…` is the only form that cannot change under you — which is why the promotion workflow
itself resolves a tag to a digest before it ships anything.

Resolve the digest behind a tag (no pull needed):

```sh
docker buildx imagetools inspect ghcr.io/partyline-sh/partyline-web:latest --format '{{.Manifest.Digest}}'
# or, without buildx:
docker manifest inspect -v ghcr.io/partyline-sh/partyline-web:latest | head
```

Then set it in the `.env` beside the compose file. There are two knobs and they are not
interchangeable:

```sh
# /opt/partyline/.env  — pin to an immutable tag
WEB_TAG=prod-1a2b3c4
RELAY_TAG=prod-1a2b3c4
```

```sh
# …or pin to the bytes. WEB_IMAGE replaces the whole reference, which is the only way to express a
# digest: `@` is not legal in the tag position, so WEB_TAG cannot carry one.
WEB_IMAGE=ghcr.io/partyline-sh/partyline-web@sha256:9f2c…
RELAY_IMAGE=ghcr.io/partyline-sh/partyline-relay@sha256:7ad1…
```

`WEB_IMAGE` / `RELAY_IMAGE` are also the seam to use if you mirror these images into a registry of
your own — set them to your reference and nothing else in the stack changes.

Whichever you use, confirm what you actually pinned before bringing the stack up:

```sh
docker compose -f deploy/stack/docker-compose.yml config | grep 'image:'
```

Upgrading is then a deliberate act: change the value, `docker compose pull && docker compose up -d`.
Rolling back is the same edit with the previous value.

## Publishing these images (maintainers only)

These packages start life private because **GHCR packages created by a workflow in a private
repository default to private**, and `partyline-sh/partyline` is private. Nothing went wrong; that
is simply the default we have to override once.

Making a GHCR package public is a **manual, one-time, per-package** action. It has no API — not a
gap in our tooling, an absence in GitHub's. Two independent APIs say so:

**REST.** A real route echoes *its own* `documentation_url`; a route that does not exist returns
the generic one. That is the discriminator, and it is validated against a route we know exists:

| Request | Status | `documentation_url` | Reading |
|---|---|---|---|
| `GET …/packages/container/partyline-web` | `403` | `…#get-a-package-for-an-organization` | real route, wrong scope |
| `DELETE …/packages/container/<absent>` | `404` | `…#delete-a-package-for-an-organization` | real route, no such package |
| `PATCH …/packages/container/partyline-web` | `404` | `https://docs.github.com/rest` | **no such route** |
| `PATCH …/partyline-web/visibility` | `404` | `https://docs.github.com/rest` | **no such route** |
| `PUT …/partyline-web/visibility` | `404` | `https://docs.github.com/rest` | **no such route** |

**GraphQL.** Of 258 mutations in the schema, the only package-related one is `deletePackageVersion`.
There is no visibility mutation.

So the write path is *absent*, not merely forbidden: no scope, no PAT and no `GITHUB_TOKEN`
permission reaches it, and no workflow can do this for you. Re-run the probes if you suspect
GitHub has added one — a specific `documentation_url` where there is now a generic one is the
signal that `scripts/public-images.sh` could become a fixer instead of a checker.

Org policy does not stand in the way: `partyline-sh` is on the free plan, which permits public
packages, and `GET /orgs/partyline-sh` exposes no package-visibility restriction.

For each of **`partyline-web` and `partyline-relay` — and nothing else**:

1. <https://github.com/orgs/partyline-sh/packages/container/partyline-web/settings>
2. Danger Zone → **Change visibility** → Public → type the package name to confirm.
3. Repeat for `partyline-relay`.
4. Verify from anywhere: `./scripts/public-images.sh`

If the **Change visibility** control is greyed out, the likely cause is the package's *Inherit
access from source repository* setting tying it to the private repo — remove the inheritance, then
change visibility. (UNVERIFIED: stated from GitHub's documented behaviour, not reproduced here, as
this needs the UI.)

Two things worth being deliberate about, because neither is reversible:

- **Publication cannot be undone.** Setting a package back to private does not un-publish bytes
  anyone already pulled, cached or mirrored. Before the first flip, satisfy yourself about what is
  in the images — `./scripts/image-secret-scan.sh ghcr.io/partyline-sh/partyline-web:prod
  ghcr.io/partyline-sh/partyline-relay:prod` scans a pulled image's filesystem, config env and
  build history. CI runs the same scan on every staging build, *between the build and the push*.
- **Flip exactly two.** The visibility control is a per-package page reached from a list where
  every package looks alike, so the realistic mistake is one too many rather than one too few.
  `scripts/public-images.sh` asserts both directions — the two are public, and the others are not.

## Check it yourself

```sh
./scripts/public-images.sh          # anonymous pull works for both images
./scripts/public-images.sh --pull   # …and actually pull them, with an empty docker config
TAG=prod ./scripts/public-images.sh # check a specific tag
```

`docker compose -f deploy/stack/docker-compose.yml config` needs a `.env` beside the compose file
(the file declares one as `env_file`); `touch deploy/stack/.env` is enough to validate the file
without configuring anything.

## What is in these images

Nothing environment-specific and nothing secret, and both are enforced rather than asserted:

- **No build-time configuration.** `NEXT_PUBLIC_*` used to be baked in at compile time, which meant
  an image carried the builder's hostname into your bundle. It is read at runtime now, and the
  release workflows carry a standing instruction never to reintroduce a build arg that identifies an
  environment — promotion ships the exact bytes the `staging` branch's build validated, so an image
  that knows where it runs breaks that promotion outright. It also means the published image is
  yours to point at your own site with nothing but `.env`.
- **No credentials.** `scripts/image-secret-scan.sh` scans every file in the built image (binaries
  included), the image config env, and the build history against the same credential-class
  catalogue the repository scanner uses, and it runs in CI **between the build and the push** — so
  the gate is in front of publication, not behind it. Every real secret arrives at runtime from your
  own `.env`; see `env.example`.
