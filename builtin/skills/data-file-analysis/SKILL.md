---
name: data-file-analysis
description: 用户只给出数据文件（@file:<id>）并要求完成某个分析时的入口技能：必须先调用 list_project_scripts 盘点当前项目已有脚本，看是否有能满足需求的脚本；有则直接用 create_script_analysis 建节点运行出结果，没有则用 ask_user 询问用户改用 create_agent_analysis（命令型节点）还是 create_script（可复用脚本）后再继续。包含读文件结构、脚本匹配判断、request_param 组织、运行失败迭代与结果汇报。用户拖入数据文件说「帮我分析这个数据」且未指定脚本时使用。
---

# 数据文件驱动的分析入口

**适用场景**：用户消息里只有 `@file:<id>` 引用（可能多个）+ 一句分析需求（如「帮我做差异分析」
「这个数据做个 PCA」「分析一下这个表」），**没有指定用哪个脚本、也没说怎么建节点**。

本技能的唯一下场是让分析真正跑起来：要么复用已有脚本，要么由用户选择创建方式后再推进，
不允许在「只查了一下」「只写了个脚本」的状态下收尾。

---

## 一、固定顺序（三条硬规则）

1. **第一步必须 `list_project_scripts`**：不带参数（项目取自当前会话的激活项目），
   拿到当前项目全部脚本的 `script_id` / `script_name` / `description` / `script_type`。
   **不得跳过这一步直接 `create_script` 或 `create_agent_analysis`。**
2. **先判断有没有满足需求的脚本**：
   - 有 → 走「三、已有匹配脚本」：直接 `create_script_analysis` 建节点并运行；
   - 没有（含 `total=0`）→ 走「四、没有可用脚本」：**用 `ask_user` 问用户选哪种创建方式**，
     不要替用户做决定。
3. **不要制造重复脚本**：能复用已有脚本时，不要再建一个功能相同的新脚本。

判断「满足需求」看三件事，缺一不可：

| 判断项 | 依据 |
| --- | --- |
| 分析目的 | 脚本 `description` / `script_name` 是否就是要做的分析（差异分析、PCA、富集、多样性…） |
| 数据适配 | 脚本 `io_schema_path` 的 inputs 是否能承接该文件类型（表格 / 矩阵 / VCF / 表达矩阵…） |
| 参数可给定 | 用户已提供或可从数据推出的参数是否满足 inputs 的必填项即可 |

只有一个大致相关、但明显做不了该分析的脚本（如要做富集却只有 PCA 脚本）→ 视为「没有可用脚本」。
多个脚本同时匹配时，取 `description` 与需求最贴合的一个；仍无法区分时用 `ask_user` 让用户选
（`single_choice`，option 的 `label` 用「脚本名 — 描述」，`value` 用 `script_id`）。

---

## 二、先读文件结构（无论走哪条路径都要做）

- 调用 `read_file_by_id`，`file_id` 取 `@file:<id>` 冒号后的数字（多个文件就逐个读）。
- 拿到 `file_path`（后续作为节点输入参数的真实路径）、`format`、`sheets[].columns` 与预览行。
- 用列名 / 取值确认数据能否支撑用户的需求；**输入文件路径一律用返回的 `file_path`**，
  不要用 `find` 扫描工作区猜路径。
- 若需求涉及分组（实验组 / 对照组、处理 / 未处理等）而分组列或组含义不明确，
  先按 `analysis-script-authoring` 技能的「生物分组不明确时必须先 ask_user」提问，
  把真实候选列名与取值写进问题里。

---

## 三、已有匹配脚本：直接 `create_script_analysis`

1. 用 `get_script`（`script_id` + `io_schema_path`）确认该脚本 inputs 的 **name 与类型**，
   参数键一律以 `io_schema.json` 为准，不要凭脚本名或经验发明参数名。
2. 调用 `create_script_analysis`：
   - `script_id`：匹配到的脚本 ID；
   - `node_name`：按本次需求起名（如 `deseq2_<数据名>`），同一脚本多次分析时便于区分；
   - `request_param`：键 = io_schema `inputs[].name`；文件类输入填 `read_file_by_id` 返回的
     `file_path`（`FileSelect` 的对象形态见 `analysis-io-schema-spec`）；数字 / 布尔用原生
     JSON 类型，`BaseTextAreaNum` 用逗号分隔字符串；
   - `run` 默认 `true`（创建后立即运行并返回 status / success / log）；大文件可加大 `timeout_seconds`。
3. 运行失败（`success=false` 或带 `error`）：先读 `log_path` 定位原因；
   **参数写错用 `build_analysis_node_params` 改参数后重跑**，不要为此新建脚本或新节点。
4. `success=true` 且产物符合脚本 `outputs` 后，向用户汇报：节点名称 / ID、工作目录、
   产物路径（`output/` 下文件）、结果要点（行数、显著条目数、主要结论），必要时提示可继续做的下游分析。

---

## 四、没有可用脚本：先问用户，再按选择推进

用 `ask_user` 提问（一次问清，不要反复打断用户），把两种方式的差异讲明白：

- `questions[0].id` 用 `analysis_mode`，`type` 用 `single_choice`，至少给两个 option：
  - `label`: 「命令型节点（create_agent_analysis）」，
    `value`: `agent_analysis`，
    说明：一次性分析，直接写 shell / R / python 命令并跑出结果，产物落在 `output/`，不沉淀可复用脚本。
  - `label`: 「分析脚本 + 节点（create_script）」，
    `value`: `script`，
    说明：把分析写成可复用脚本（含 io_schema 参数表单），之后可在本项目反复用、可配参数。
- 用户可能补充分析细节，同时用 `text` 问题（如 `analysis_goal`）确认目标（比较什么、阈值、输出要什么），
  能一次问清的问题合并到同一次 `ask_user` 调用里。

拿到答复后：

- **`agent_analysis`** → 按 `create_agent_analysis` 的工具描述写 `command`：
  以 `mkdir -p output` 开头，成果写到 `output/` 第一级，中间文件放 `output/tmp/`；
  重要表格 / 文本产物登记到 `output/outputs.json`（不登记图片与日志）；输入路径直接用
  `read_file_by_id` 返回的 `file_path`。创建后默认立即运行，失败先读 `log_path`，
  再用 `update_agent_analysis` 或改 `command_path` 后 `run_analysis_node` 修复，不要反复新建节点。
- **`script`** → 按 `analysis-script-authoring` 技能的固定流程推进：
  `create_script`（一次给出真实 inputs / outputs）→ 确认 `io_schema_path` 契约 →
  写 `script_file_path` 脚本主体 → `create_script_analysis` 建节点并运行 →
  失败迭代（`build_analysis_node_params` / 改脚本）→ 直到 `run_analysis_node` 返回 success。
- 用户选不到、答复含糊或直接反问时，按 `ask_user` 再澄清一次；**不得自行假定一种方式继续**。

---

## 五、禁止事项

- 未调用 `list_project_scripts` 就建脚本或命令型节点。
- 存在能做该分析的脚本时，另外新建一个重复脚本。
- 没有可用脚本时，不询问用户就直接选一种创建方式。
- 凭脚本名猜参数名，或把 `@file:<id>` 当作文件路径传给节点（必须先 `read_file_by_id` 拿 `file_path`）。
- 用 `find` 全盘扫描工作区找输入文件；路径取自 `read_file_by_id`、`params.json`、上游节点产物。
- 只建了脚本或节点就宣布完成：任务完成的标准是节点运行 `success=true` 且产物符合 `outputs`。
