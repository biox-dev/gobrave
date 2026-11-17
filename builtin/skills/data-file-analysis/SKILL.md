---
name: data-file-analysis
description: 用户只给出数据文件（@file:<id>）并要求完成某个分析时的入口技能：先 list_project_scripts 盘点项目脚本 + get_file 看文件内容；有可用脚本则用 get_script 读其 README（脚本使用方法、参数说明、示例 request_param）直接组织参数建节点运行（节点名必须以输入文件名去扩展名为前缀，如 spleen_gene_count_SP_vs_SA），README 为空或不可用时才读 script_file_path 与 io_schema_path；再 decide 用 create_analysis_node_script 渲染跑结果。没有可用脚本则 ask_user 让用户在 create_analysis_node_agent 与 create_script 之间选。用户拖入数据文件说「帮我分析这个数据」且未指定脚本时使用。
---

# 数据文件驱动的分析入口

**适用场景**：用户消息里只有 `@file:<id>` 引用（可能多个）+ 一句分析需求（如「帮我做差异分析」
「这个数据做个 PCA」「分析一下这个表」），**没有指定用哪个脚本、也没说怎么建节点**。

本技能的唯一下场是让分析真正跑起来：要么复用已有脚本，要么由用户选择创建方式后再推进，
不允许在「只查了一下」「只写了个脚本」的状态下收尾。

目标链路（**四步，尽量两轮工具调用完成**）：

~~~text
list_project_scripts（脚本清单）  +  get_file（文件内容）        ← 首轮并行
        ↓ 找到匹配脚本
get_script（readme_content = 脚本使用方法）
        ↓ README 为空 / 不可用
读 script_file_path + io_schema_path（兜底才读）
        ↓
create_analysis_node_script(request_param, run=true)  →  出结果
~~~

---

## 零、四条硬规则（违反即返工）

### 1. 首轮只并行两件事，且必须同一轮发出

`list_project_scripts` + `get_file` **必须在同一个 function_calls 块里并行发出**，
禁止「先 list → 等返回 → 再读文件 → 等返回」这种串行试探。

- 首轮**不要**同时 `head` io_schema.json 或 `grep` 脚本主体：`get_script` 的 `readme_content`
  通常已包含参数说明与示例 request_param，直接读 README 比翻脚本更快、更省。
- 只有 README 缺失 / 不可用时，才在后续轮次补 `head io_schema.json` 与
  `grep -n 'params\$' <script_file_path>`（python 用 `grep -n 'params\[' main.py`）。

### 2. README 优先，脚本体是兜底

`get_script` 一次返回 `script_name` / `description` / `readme_file` / `readme_content` /
`script_file_path` / `io_schema_path` / `container_template_id`。**先看 `readme_content`**：

- **可用**（非空，且能看到参数说明、输入要求或 `request_param` 示例）→ 直接据此组织参数建节点，
  **不要再读 io_schema、也不要 grep 脚本**。
- **不可用**（空、只有标题一行、或没写清参数）→ 才读 `io_schema_path` + grep `script_file_path`，
  搞清楚必须提供哪些键，然后再建节点。

顺手把「README 不可用」当成待改进项：跑完后建议用户（或直接）给该脚本补 README，
下次同类需求就只需 `get_script` 一轮。

### 3. 先跑后查：建节点前不做数据体检

第一次 `create_analysis_node_script` / `create_analysis_node_agent` 之前，**禁止**用 `awk` / `sort | uniq -d` /
`cut | sort -u` 之类命令体检数据（查重名基因、空值、唯一性、分组取值分布……）。
`get_file` 给出的列名 + 前几行足够判断数据能否支撑需求，参数按列名直接给，
让脚本自己去碰数据；**跑不通再查**，且优先看 `log_path` 里的真实报错定位，不要做事前「预防性」扫描。

### 4. 脚本自扛：`make.unique` 类问题由脚本兜住，不靠事前检查

表达矩阵里基因名重复是常态，不要因为「可能有重名」就停下来体检或追问用户：

- 脚本里对基因名走 `make.unique()`（R）等去重即可，不要为此加拦截或直接报错退出；
- 需要稳定标识时**重名回退 `gene_id`**（如 `ifelse(duplicated(gene_name), gene_id, gene_name)`），
  保证行名唯一后继续往下跑；
- 空值 / 缺失同理：脚本里 `drop_na()` / `na.omit()` 兜住，或让下游直接报错，再按 `log` 定向修。

原则：能被脚本一行兜住的问题，不要升级成一次数据体检或一次追问。

### 5. 节点名一律带输入文件名前缀

`create_analysis_node_script` 的 `node_name` 必须写成 **`<输入文件名去扩展名>_<分析命名>`**
（如 `spleen_gene_count_SP_vs_SA`），让同一脚本在不同数据上的节点在列表里一眼可辨。
文件名取 `get_file` 的 `file_name`，多个输入时取主数据文件名。这条对
`create_analysis_node_agent` 同样适用（命令型节点命名也带文件名前缀）。

---

## 一、第一轮：盘点脚本 + 读文件

同一轮并行发出两个调用：

1. `list_project_scripts`（不带参数，项目取自当前会话的激活项目）→ 全部脚本的
   `script_id` / `script_name` / `description` / `script_type` / `script_file_path` / `io_schema_path`。
   **不得跳过这一步直接 `create_script` 或 `create_analysis_node_agent`。**
2. `get_file`，`file_id` 取 `@file:<id>` 冒号后的数字（多个文件就逐个读）→
   `file_name` / `file_path`（后续作为节点输入的真实路径）/ `format` / 列名与预览行。

用列名 / 取值确认数据能否支撑用户的需求；**输入文件路径一律用返回的 `file_path`**，
不要用 `find` 扫描工作区猜路径。

若需求涉及分组（实验组 / 对照组、处理 / 未处理等）而分组列或组含义不明确，
先按 `analysis-script-authoring` 技能的「生物分组不明确时必须先 ask_user」提问，
把真实候选列名与取值写进问题里。

---

## 二、判断有没有可用脚本

看三件事，缺一不可：

| 判断项 | 依据 |
| --- | --- |
| 分析目的 | 脚本 `description` / `script_name` 是否就是要做的分析（差异分析、PCA、富集、多样性…） |
| 数据适配 | 脚本 README / `io_schema_path` 的 inputs 是否能承接该文件类型（表格 / 矩阵 / VCF / 表达矩阵…） |
| 参数可给定 | 用户已提供或可从数据推出的参数是否满足必填项 |

- 有匹配 → 走「三、已有匹配脚本」。
- 没有（含 `total=0`）→ 走「四、没有可用脚本」：**用 `ask_user` 问用户选哪种创建方式**。
- 只有一个大致相关、但明显做不了该分析的脚本（如要做富集却只有 PCA 脚本）→ 视为「没有可用脚本」。
- 多个脚本同时匹配时，取 `description` 与需求最贴合的一个；仍无法区分时用 `ask_user` 让用户选
  （`single_choice`，option 的 `label` 用「脚本名 — 描述」，`value` 用 `script_id`）。
- **不要制造重复脚本**：能复用已有脚本时，不要再建一个功能相同的新脚本。

---

## 三、已有匹配脚本：`get_script` → 组织参数 → 建节点

### 1. `get_script(script_id)` 拿使用方法

一次拿全 `readme_content`（使用方法）/ `io_schema_path` / `script_file_path` / `container_template_id`。

### 2. 按 README 组织 `request_param`

README 里通常会写明：输入数据要求、参数表（键 / 类型 / 说明）、**可直接复制的示例 request_param**、
产物说明。照抄示例、把文件 ID 与样本名换成当前数据的即可。

组织参数时必须遵守：

- 键 = `io_schema` `inputs[].name`，**再叠加 README / 脚本里声明的未声明扁平键**
  （如 `__gene_sig_type` / `__gene_sig_threshold` / `__gene_effect_threshold` / `analysis_name`）。
  这类复合控件落成的扁平键**不会**由 `gene: {sig_type: ...}` 对象自动展开，漏给会直接报
  `argument is of length zero`；`analysis_name` 之类命名键不可为 null / 空串。
- `FileSelect` 的 `file` 填 **`@file:<id>` 冒号后的文件 ID 字符串**（不是 path，path 由后端解析进
  `params.<name>.path`），对象形态见 `analysis-io-schema-spec`；
- 数字 / 布尔用原生 JSON 类型，`BaseTextAreaNum` 用逗号分隔字符串；
- 文本控件（如火山图标题 / 分析名称）不硬编码模板默认值，按本次比较内容填写。

### 3. README 不可用时才读脚本内部

`head -c 2000 <io_schema_path>` 看 inputs 条目，`grep -n 'params\$' <script_file_path>`（qmd / R）
或 `grep -n 'params\[' <script_file_path>`（python）确认 params.json 必须提供哪些键。
不要凭脚本名或经验发明参数名。

### 4. 建节点并运行

```
create_analysis_node_script(
  script_id: "<匹配到的脚本 ID>",
  node_name: "<输入文件名去掉扩展名>_<本次分析命名>",
  request_param: <按上面组织好的参数>,
  run: true
)
```

**`node_name` 必须以输入文件名前缀开头**：取 `get_file` 返回的 `file_name` 去掉扩展名
（如 `spleen_gene_count.xls` → `spleen_gene_count`），后面接分析命名，用 `_` 连接，
例如 `spleen_gene_count_SP_vs_SA`、`lung_tpm_PCA`。多个输入文件时用主数据（表达矩阵 /
变异表）的文件名；文件名过长或含空格 / 特殊字符时先规整成下划线形式。
不要只用分析名（如 `SP_vs_SA`）或脚本名当节点名。

- `run` 默认 `true`：一次建好就跑完并返回 status / success / log。**不要先 run=false 再补
  `build_analysis_node_params` + `run_analysis_node`**；大文件可加大 `timeout_seconds`。
- 同一脚本多次比较：每次建独立节点、各给不同的 `node_name` 与命名类参数（如 `analysis_name`），
  产物文件名互不覆盖；这些互不依赖的节点可以在同一轮并行发出。

### 5. 失败迭代

`success=false` 或带 `error`：先读 `log_path` 定位原因；
**参数写错用 `build_analysis_node_params` 改参数后重跑**，不要为此新建脚本或新节点；
脚本逻辑错才改 `script_file_path` 后 `run_analysis_node`。

### 6. 汇报

`success=true` 且产物符合脚本 `outputs` 后，向用户汇报：节点名称 / ID、工作目录、
产物路径（`output/` 下文件）、结果要点（行数、显著条目数、主要结论），必要时提示可继续做的下游分析。

---

## 四、没有可用脚本：先问用户，再按选择推进

用 `ask_user` 提问（一次问清，不要反复打断用户），把两种方式的差异讲明白：

- `questions[0].id` 用 `analysis_mode`，`type` 用 `single_choice`，至少给两个 option：
  - `label`: 「命令型节点（create_analysis_node_agent）」，
    `value`: `agent_analysis`，
    说明：一次性分析，直接写 shell / R / python 命令并跑出结果，产物落在 `output/`，不沉淀可复用脚本。
  - `label`: 「分析脚本 + 节点（create_script）」，
    `value`: `script`，
    说明：把分析写成可复用脚本（含 io_schema 参数表单与 README 使用方法），之后可在本项目反复用、可配参数。
- 用户可能补充分析细节，同时用 `text` 问题（如 `analysis_goal`）确认目标（比较什么、阈值、输出要什么），
  能一次问清的问题合并到同一次 `ask_user` 调用里。

拿到答复后：

- **`agent_analysis`** → 按 `create_analysis_node_agent` 的工具描述写 `command`：
  以 `mkdir -p output` 开头，成果写到 `output/` 第一级，中间文件放 `output/tmp/`；
  重要表格 / 文本产物登记到 `output/outputs.json`（不登记图片与日志）；输入路径直接用
  `get_file` 返回的 `file_path`。创建后默认立即运行，失败先读 `log_path`，
  再用 `update_analysis_node_agent` 或改 `command_path` 后 `run_analysis_node` 修复，不要反复新建节点。
- **`script`** → 按 `analysis-script-authoring` 技能的固定流程推进：
  `create_script`（一次给出真实 inputs / outputs）→ 确认 `io_schema_path` 契约 →
  写 `script_file_path` 脚本主体 → **同时写 `readme_file`（README.md）**：
  写清输入数据要求、参数表（键 / 类型 / 说明）、可直接复制的示例 `request_param`、产物说明、
  以及「哪些扁平键必须平铺」这类坑 → `create_analysis_node_script` 建节点并运行 →
  失败迭代（`build_analysis_node_params` / 改脚本）→ 直到 `run_analysis_node` 返回 success。
- 用户选不到、答复含糊或直接反问时，按 `ask_user` 再澄清一次；**不得自行假定一种方式继续**。

---

## 五、禁止事项

- 未调用 `list_project_scripts` 就建脚本或命令型节点。
- 存在能做该分析的脚本时，另外新建一个重复脚本。
- 有匹配脚本时不去 `get_script` 看 README，反而先 grep 脚本主体 / 翻 io_schema 拼参数。
- README 已写清参数与示例时，仍逐层读 `io_schema.json`、逐行读脚本主文件。
- 没有可用脚本时，不询问用户就直接选一种创建方式。
- 凭脚本名猜参数名，或把 `@file:<id>` 当作文件路径传给节点（必须先 `get_file` 拿 `file_path`）。
- 用 `find` 全盘扫描工作区找输入文件；路径取自 `get_file`、`params.json`、上游节点产物。
- 首轮只发 `list_project_scripts` 一个调用，把 `get_file` 留到后面几轮串行做。
- 建节点前用 `awk` / `uniq -d` / `sort -u` 做数据体检（重名、空值、唯一性、分组分布），
  或因为「可能有重名基因」就停下来追问用户；这类问题交给脚本 `make.unique()` / 回退 `gene_id` 兜住。
- 只建了脚本或节点就宣布完成：任务完成的标准是节点运行 `success=true` 且产物符合 `outputs`。
- 建节点时 `node_name` 只用分析名 / 脚本名，不带输入文件名前缀（正确形如 `spleen_gene_count_SP_vs_SA`）。
