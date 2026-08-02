<p align="center">
  <img src="assets/logo.svg" alt="gobrave" width="520">
</p>

<p align="center">
  <h1 align="center">gobrave</h1>
  <p align="center"><strong>Bioinformatics Reactive Analysis & Visualization Engine — built in Go</strong></p>
</p>

<p align="center">
  <a href="https://gobrave.dev"><img src="https://img.shields.io/badge/docs-gobrave.dev-00ADD8?style=flat-square&logo=go" alt="docs"></a>
  <a href="https://github.com/gobravedev/gobrave/blob/main/LICENSE"><img src="https://img.shields.io/badge/license-MIT-blue.svg?style=flat-square" alt="license"></a>
  <img src="https://img.shields.io/badge/go-%3E%3D1.25-00ADD8?style=flat-square&logo=go" alt="go version">
  <img src="https://img.shields.io/badge/status-active-success?style=flat-square" alt="status">
</p>

---

## 🧬 What is gobrave?

**gobrave** is a high-performance bioinformatics platform that bridges the gap between **pipeline engineering** and **scientific discovery**. Built from the ground up in Go, it delivers unparalleled speed, a single-binary deployment, and a modern architecture designed for scale.

> 🚀 One binary. Zero friction. From raw data to publication-ready insights.

```mermaid
graph LR
    A[🧪 Upload Data] --> B[📊 Build DAG Pipeline]
    B --> C[🐳 Containerized Execution]
    C --> D[📈 Interactive Visualization]
    D --> E[🤖 AI Copilot Assistance]
    C --> F[🌐 Real-time Streaming]
```

---

## ✨ Why gobrave?

| Traditional Platforms | ⚡ gobrave |
|---|---|
| Multi-process deployment | **Single static binary** |
| Complex dependency management | **Zero runtime deps** |
| Limited concurrency model | **Native goroutine parallelism** |
| Heavy memory footprint | **~30 MB memory baseline** |
| Lengthy install & config | **Download & run** |

---

## 🎯 Core Capabilities

### ⚙️ Visual DAG Workflow Engine
Design complex bioinformatics pipelines as **directed acyclic graphs** — no code required.

- 🎨 Drag-and-drop node composition (R, Python, Shell)
- 🔄 Automatic upstream → downstream data propagation
- 💾 Smart caching: skip unchanged nodes, re-run only what's needed
- 🧩 Scatter/gather patterns for parallel sample processing
- ♻️ Crash recovery with heartbeat-based lease locking

### 🐳 Containerized Analysis Workspaces
Launch full analysis environments **on demand**, right from your pipeline.

- **RStudio Server** · **JupyterLab** · **VS Code Server** — one-click launch
- Docker & Kubernetes runtime support
- Auto-provisioned reverse proxy (Traefik / K8s Ingress / built-in gateway)
- User-namespaced, isolated containers with host UID mapping

### 🤖 Built-in AI Copilot
An LLM-powered assistant embedded directly in the platform.

- Streaming chat via **WebSocket** (real-time token delivery)
- Permission-gated tool execution (write files, run commands)
- Session-aware context with persistent conversation history
- Powered by Copilot SDK — extensible to any LLM backend

### 🌐 Real-time Collaboration
Stay in sync with every event in the system.

- **WebSocket + SSE** dual-transport realtime hub
- Per-user connection pooling with automatic eviction
- Live DAG execution progress, node status, and container events
- Frontend actions bridged via unified event bus

### 📁 Data Management
Organize your research artifacts with project-scoped CRUD.

- Datasets, Samples, Files — hierarchical, cross-referenced
- Role-based file annotation (e.g. FASTQ_R1, FASTQ_R2)
- Bulk import & project-level listing
- Transactional integrity with cascading cleanup

### 🔐 Enterprise-Ready
- JWT + API Key dual authentication
- Multi-tenant isolation
- OnlyOffice collaborative document editing
- Swagger auto-generated API docs

---

## 🏗️ Architecture

```
┌─────────────────────────────────────────────────────────┐
│                     Frontend (SPA)                       │
├─────────────────────────────────────────────────────────┤
│  REST API  │  WebSocket  │  SSE  │  Reverse Proxy       │
├─────────────────────────────────────────────────────────┤
│  ┌─────────┬──────────┬──────────┬──────────────────┐   │
│  │   Auth   │ Project  │   Data   │    Analysis      │   │
│  ├─────────┼──────────┼──────────┼──────────────────┤   │
│  │ Container│ Workflow │ Realtime │    LLM Bridge    │   │
│  └─────────┴──────────┴──────────┴──────────────────┘   │
├─────────────────────────────────────────────────────────┤
│                  DAG Orchestrator                        │
│  ┌──────────┬──────────┬──────────┬────────────────┐    │
│  │ Scheduler│Dispatcher│WorkerPool│ State Machine  │    │
│  └──────────┴──────────┴──────────┴────────────────┘    │
├─────────────────────────────────────────────────────────┤
│              Container Runtime (Docker / K8s)            │
├─────────────────────────────────────────────────────────┤
│              Route Registry (Traefik / Gateway)          │
└─────────────────────────────────────────────────────────┘
```

---

## 🚀 Quick Start

### Prerequisites
- Go **≥ 1.25**
- MySQL **8.0+**
- Docker / Kubernetes / k3s (for containerized execution)

### Install & Run

```bash
# Clone
git clone https://github.com/gobravedev/gobrave.git
cd gobrave

# Configure
cp config.example.yml config.yml
# Edit config.yml with your database & settings

# Run (it's that simple)
go run ./cmd/server
```



---

## 📖 Documentation

Full documentation at **[gobrave.dev](https://gobrave.dev)**

- [Getting Started](https://gobrave.dev/docs/getting-started)
- [DAG Pipeline Guide](https://gobrave.dev/docs/dag)
- [Container Workspaces](https://gobrave.dev/docs/containers)
- [AI Copilot](https://gobrave.dev/docs/copilot)
- [API Reference](https://gobrave.dev/docs/api)

---

## 🤝 Contributing

We welcome contributions! See [CONTRIBUTING.md](CONTRIBUTING.md) for guidelines.

---

## 📄 License

gobrave is open-source under the [MIT License](LICENSE).

---

<p align="center">
  <sub>Built with ❤️ by the <a href="https://github.com/gobravedev">gobrave team</a> · Powered by Go</sub>
</p>
