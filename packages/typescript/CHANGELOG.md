# Changelog

## 1.0.0 (2026-10-07)


### Features

* add @mogenius/sandbox with lifecycle, executeCommand and codeRun ([2762e42](https://github.com/mogenius/mo-sandbox-sdk/commit/2762e42336ae83e7db6188123e10dc9feb21cc2e))
* add the Go SDK and return matplotlib figures from codeRun as charts ([115a3f6](https://github.com/mogenius/mo-sandbox-sdk/commit/115a3f646ce0303d84509b00e8fe48ea6832e2e8))
* stream fs downloads through a one-time link with buffered fallback ([5ce5d24](https://github.com/mogenius/mo-sandbox-sdk/commit/5ce5d241357e3091bea02f98bd8f2397366a1ec8))
* tunnel to sandbox ports, exec and files on the pod routes, choose the container ([51d5571](https://github.com/mogenius/mo-sandbox-sdk/commit/51d557142556c99fd89e88c557e857c7c18b0ec9))


### Bug Fixes

* bump vitest to v5 to clear tinypool and mocker advisories; stream a command's output live with executeCommandStream ([5bccf45](https://github.com/mogenius/mo-sandbox-sdk/commit/5bccf4586150b52b4b1eb749677df9ec52468aa9))
* describe the SDK on its own terms and add the typedoc reference ([3c049ae](https://github.com/mogenius/mo-sandbox-sdk/commit/3c049aee2ec76bee58f8bf0ff44f872f0a826cd7))
* map the unprefixed pod error codes — packages/typescript/src/errors.ts ([07d6757](https://github.com/mogenius/mo-sandbox-sdk/commit/07d67577bf6d9e427df2800fd218ff37e18c1d13))
* publish to GitHub Packages like client-sdk and server-sdk ([cca4ff2](https://github.com/mogenius/mo-sandbox-sdk/commit/cca4ff2df29fae5ef94d4a5fc4bc2637bc6d9ac2))
* resume interrupted downloads with range requests ([8e2d22d](https://github.com/mogenius/mo-sandbox-sdk/commit/8e2d22d57c669639def3913c317b61192e5e351b))
* run sandbox sessions on the pod session routes, with live command logs ([393f965](https://github.com/mogenius/mo-sandbox-sdk/commit/393f9657c09076ef6b374e25bed20caba77fa7cd))
* send the exec request as a frame once the gateway asks for it — CLAUDE.md, src/process.ts, test/process-stream.test.ts. ([6446e93](https://github.com/mogenius/mo-sandbox-sdk/commit/6446e93d2f7919d3afc7f5007353a9e981b434c5))
