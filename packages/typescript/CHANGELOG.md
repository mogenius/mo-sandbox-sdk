# Changelog

## 1.0.0 (2026-10-30)


### Features

* add @mogenius/sandbox with lifecycle, executeCommand and codeRun ([2762e42](https://github.com/mogenius/mo-sandbox-sdk/commit/2762e42336ae83e7db6188123e10dc9feb21cc2e))
* add the Go SDK and return matplotlib figures from codeRun as charts ([115a3f6](https://github.com/mogenius/mo-sandbox-sdk/commit/115a3f646ce0303d84509b00e8fe48ea6832e2e8))
* auto-stop interval, refresh activity and persistent in both SDKs ([6720a92](https://github.com/mogenius/mo-sandbox-sdk/commit/6720a924ffac25ba0d41ad8fd24f091cd27edf67))
* configurable default warm pool (MOGENIUS_SANDBOX_PROFILE) ([718f02b](https://github.com/mogenius/mo-sandbox-sdk/commit/718f02b806271d00a72d42bcd9861c144086fff9))
* configurable default warm pool (MOGENIUS_SANDBOX_PROFILE) ([e857a5a](https://github.com/mogenius/mo-sandbox-sdk/commit/e857a5ab2840b1bc9b17ab786c63b36b5cac7f73))
* env for sessions (WithSessionEnv, createSession options), chat example ([d3e0914](https://github.com/mogenius/mo-sandbox-sdk/commit/d3e09140bbf799c10f6f19c7f837247c6eb77508))
* full parity of both SDKs, Go Ephemeral as *bool, Node 24+, docs and examples for every call ([7f3f184](https://github.com/mogenius/mo-sandbox-sdk/commit/7f3f184ec23ad6aa2c6e60dab493f829eb46c20d))
* list warm pools of a namespace or the whole cluster (WarmPools, warmPools) ([4bd4f32](https://github.com/mogenius/mo-sandbox-sdk/commit/4bd4f32aad309a2fb3103826a34d7c5014faff36))
* session commands report truncated output ([fd6462e](https://github.com/mogenius/mo-sandbox-sdk/commit/fd6462e49253c2a097b9e54f16515df3fcef12fb))
* stream fs downloads through a one-time link with buffered fallback ([5ce5d24](https://github.com/mogenius/mo-sandbox-sdk/commit/5ce5d241357e3091bea02f98bd8f2397366a1ec8))
* tunnel to sandbox ports, exec and files on the pod routes, choose the container ([51d5571](https://github.com/mogenius/mo-sandbox-sdk/commit/51d557142556c99fd89e88c557e857c7c18b0ec9))
* uploads over SFTP through the stream gateway (transport option, client default, SFTP_UNAVAILABLE), and an UNAUTHORIZED stream close is a forbidden error ([57f0a32](https://github.com/mogenius/mo-sandbox-sdk/commit/57f0a32443d79f28422134684ab4ee52d664f17c))


### Bug Fixes

* bump vitest to v5 to clear tinypool and mocker advisories; stream a command's output live with executeCommandStream ([5bccf45](https://github.com/mogenius/mo-sandbox-sdk/commit/5bccf4586150b52b4b1eb749677df9ec52468aa9))
* **deps:** update dependency tsup to v9 ([4a2a956](https://github.com/mogenius/mo-sandbox-sdk/commit/4a2a956406b5e9971df55d98312183b65a2a674e))
* **deps:** update dependency tsup to v9 ([9e5a03e](https://github.com/mogenius/mo-sandbox-sdk/commit/9e5a03e6bacbfafa1354ba45cd05bfd9c8d06364))
* **deps:** update dependency typescript to v6 ([599b019](https://github.com/mogenius/mo-sandbox-sdk/commit/599b019be9bf225a692d00b326f7c3f039587327))
* **deps:** update dependency typescript to v6 ([d17e99c](https://github.com/mogenius/mo-sandbox-sdk/commit/d17e99c644e463290a116a929fc3106c202d370d))
* describe the SDK on its own terms and add the typedoc reference ([3c049ae](https://github.com/mogenius/mo-sandbox-sdk/commit/3c049aee2ec76bee58f8bf0ff44f872f0a826cd7))
* map the unprefixed pod error codes — packages/typescript/src/errors.ts ([07d6757](https://github.com/mogenius/mo-sandbox-sdk/commit/07d67577bf6d9e427df2800fd218ff37e18c1d13))
* publish to GitHub Packages like client-sdk and server-sdk ([cca4ff2](https://github.com/mogenius/mo-sandbox-sdk/commit/cca4ff2df29fae5ef94d4a5fc4bc2637bc6d9ac2))
* resume interrupted downloads with range requests ([8e2d22d](https://github.com/mogenius/mo-sandbox-sdk/commit/8e2d22d57c669639def3913c317b61192e5e351b))
* run sandbox sessions on the pod session routes, with live command logs ([393f965](https://github.com/mogenius/mo-sandbox-sdk/commit/393f9657c09076ef6b374e25bed20caba77fa7cd))
* send the exec request as a frame once the gateway asks for it — CLAUDE.md, src/process.ts, test/process-stream.test.ts. ([6446e93](https://github.com/mogenius/mo-sandbox-sdk/commit/6446e93d2f7919d3afc7f5007353a9e981b434c5))
