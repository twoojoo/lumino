# Kubeon releases

Downloads of **Kubeon**, a native Kubernetes IDE. This repository only hosts
release packages; the source code is not public.

Get the latest version from [Releases](https://github.com/twoojoo/kubeon-releases/releases/latest):

| OS | Package |
|---|---|
| Linux (x86_64) | `.AppImage` (portable), `.deb`, `.rpm`, `.tar.gz` |
| Windows 10+ (x64, ARM64) | portable `.zip` |
| macOS 12+ (Apple silicon and Intel) | `.dmg` or `.zip` |

Kubeon updates itself from here (Preferences → Updates). Installs from
`.deb`/`.rpm` are updated through the package manager.

## Verifying a download

`SHA256SUMS.txt` lists the checksum of every file and is signed with the
Kubeon release key (ed25519, `SHA256SUMS.txt.sig`, base64). Public key:

```
pX1Dosf+ulIw6K2Q6uyYvCbwz99oGrYDw3AmXOLltvg=
```

The self-updater refuses any file that doesn't match a checksum signed with
this key. To check by hand: `sha256sum -c SHA256SUMS.txt --ignore-missing`,
then verify the signature with any ed25519 tool, e.g. in Python with
`cryptography`:

```python
import base64
from cryptography.hazmat.primitives.asymmetric.ed25519 import Ed25519PublicKey
key = Ed25519PublicKey.from_public_bytes(base64.b64decode("pX1Dosf+ulIw6K2Q6uyYvCbwz99oGrYDw3AmXOLltvg="))
key.verify(base64.b64decode(open("SHA256SUMS.txt.sig").read()), open("SHA256SUMS.txt", "rb").read())
print("signature ok")
```

Third-party licenses: `THIRD_PARTY_NOTICES.txt` in every package and release.
