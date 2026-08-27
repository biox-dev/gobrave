# Contributing to gobrave

Thanks for your interest in contributing to gobrave.

## Development Flow

1. Fork this repository and create your feature branch.
2. Make code changes and tests.
3. Open a Pull Request to `main` (or the default protected branch).

## Release by Git Tag

This repository has a GitHub Actions workflow that automatically publishes a GitHub Release when a tag is pushed.

Workflow file:
- `.github/workflows/tag-release.yml`

When triggered by a tag push, it will:
1. Build frontend assets from `gobravedev/brave-ui` and copy them into `web/`.
2. Build embedded single binary with frontend:
   - `go build -tags embed_frontend -o gobrave .`
3. Build normal backend binary:
   - `go build -o gobrave .`
4. Package release artifacts and publish to GitHub Releases.

Published artifacts (3 files):
1. `gobrave-<tag>-linux-amd64-embed`
2. `gobrave-<tag>-linux-amd64-with-web.zip`
3. `gobrave-<tag>-source.zip`

### How to create and push a tag

Use annotated tags for releases.

```bash
# Make sure you are on the correct commit
git pull

# Create an annotated tag (example)
git tag -a v0.1.0 -m "release v0.1.0"

# Push only this tag
git push origin v0.1.0
```

Or push all local tags:

```bash
git push origin --tags
```

### Verify release result

After pushing the tag:
1. Go to GitHub `Actions` tab and confirm workflow `Tag Release Artifacts` succeeds.
2. Go to GitHub `Releases` page and check all 3 artifacts are uploaded.

### If tag was created by mistake

Delete local and remote tag:

```bash
git tag -d v0.1.0
git push origin :refs/tags/v0.1.0
```

Then create the correct tag and push again.
