---
name: analysis-script-authoring
description: 脚本创建与修改规范总入口：从 create_script 建脚本、确认 io_schema.json 契约、写脚本主体、写 README.md（request_param 规范）、到 create_analysis_node_script 建节点、build_analysis_node_params 组参数、run_analysis_node 跑通并迭代的固定流程；包含 io_schema.json 生成规则（inputs 组件类型、outputs 的 role、FileSelect 输入输出写法、request_param 与 params.json 的对应）、README.md 的固定模板与 request_param 写法要求（可直接复制的参数示例、扁平键、FileSelect 对象形态、产物登记、常见报错）、在已创建节点里写 / 改脚本代码的约定（最简写法、可复用解析器优先而非手工切分（GFF 用 rtracklayer、表格用 readr）、tidyverse 读表与 left_join/pivot_longer、列名归一化与列表列压平、knitr::kable 打印、按 outputs 写 outputs.json）、软件安装规范（脚本里直接使用 R 包 / Python 包 / 生信软件、不提前探测安装，只有运行报错提示包或软件不存在时才用 terminal_exec 检查并尝试装到系统指定的包目录；生信软件如 prokka 优先用 mamba 安装、装进以软件名命名的 conda 环境，调用一律用 mamba run -n <env> / conda run -n <env> 命令而不写死安装 bin 绝对路径；容器基础镜像里没有 rscript / python / conda 命令时不要安装而是告知用户、网络与系统库问题先反馈用户）、生物分组不明确时必须先 ask_user、失败迭代与重复性错误写入 memory。用户要求新建分析脚本 / 分析节点、修改脚本、补充输出、写代码、写 R 代码、写 README、安装脚本依赖或运行节点时使用。
---

## 脚本创建与修改规范（总入口）

本技能是「创建 / 修改分析脚本与分析节点」的唯一规范入口，覆盖六件事：

1. **流程**：从建脚本到节点跑通要走的固定顺序；
2. **契约**：io_schema.json 怎么写、request_param 怎么填；
3. **代码**：脚本主体怎么写、产物怎么按 outputs 输出；
4. **README**：README.md 怎么写，才能让后续 agent 只读 README 就直接调
   `create_analysis_node_script` 跑通（详见第三节）；
5. **运行**：怎么跑、失败了怎么迭代；
6. **依赖**：脚本里直接使用 R 包 / Python 包 / conda 生信软件，只有运行报错提示依赖不存在时才检查安装、
   什么情况必须停下来找用户（详见第六节）。

> README.md 与 io_schema.json 是同一份契约的两个视图：io_schema 给机器解析参数，
> README 给人和其他 agent 快速上手。`data-file-analysis` 技能默认只读 `get_script` 返回的
> `readme_content` 就组织 request_param，**README 写不清楚会直接导致后续分析跑不通**。

> `inputs[]` 的逐字段细则（十种控件类型、FileSelect 完整字段表、后端解析后 params.json 的结构）
> 见 `analysis-io-schema-spec` 技能；本技能只保留流程中必须立刻用到的要点。

---

## 一、固定流程：一次任务内走完整条链路

当用户要求「新建一个分析脚本 / 分析节点」并基于数据文件跑出结果时，必须按以下顺序推进，
并且**一次任务内走完整条流程**：从创建脚本开始，一直到 `run_analysis_node` 返回 success 为止。

0) **先判断分组是否明确**：分组（实验组 / 对照组、处理组、疾病组等）是所有分析的前提。
   若无法从用户需求与数据结构中确定分组列与各组的生物学含义，**必须先调用 ask_user 向用户提问**，
   拿到答复后再进入第 1 步；不得先猜一个分组把流程跑完（详见「生物分组不明确时必须先 ask_user」）。

0.1) **确认 container_template_id（未指定就必须问用户）**：脚本要绑定哪个容器模板是所有依赖安装与运行的前提。
   - 用户消息里已明确给出容器模板（如 `@container:<id>`）或明确指定了模板 ID → 直接用；
   - **没有指定时，必须先调用 `list_container_templates` 拿到全部可运行模板**，
     再用 `ask_user`（`single_choice`）让用户选择，option 的 `label` 写「模板名 — 描述」，
     `value` 写模板 ID（`definition_id`），必要时设 `allow_custom=true` 让用户补 ID；
   - **不得自行挑选默认模板 / 凭名称猜一个写进 create_script**：模板决定了镜像里有没有 `Rscript` /
     `python` / `conda`，选错会让后面所有依赖安装白做；
   - 拿到用户选定的 ID 后，把它作为 `create_script` 的 `container_template_id` 传入，
     后续 `terminal_exec` 装包也用同一个 ID（见第六节）；
   - 用户已指定模板时不要重复提问，也不要再 `list_container_templates` 做确认。

1) `create_script` → 只创建脚本，得到 `script_id` / `script_file_path` / `io_schema_path`
   （**不会创建分析节点**）。
   - 调用时一次性给出 inputs / outputs（真实的参数契约）与 `container_template_id`（第 0.1 步用户选定的那个），
     避免创建后再补齐；也可以直接传 `script_content` / `io_schema` / `readme` 写入脚本主文件、io_schema.json、README.md。
   - **create_script 的 `inputs` 就是 io_schema.json 的 `inputs`，`outputs` 就是 io_schema.json 的 `outputs`**：
     工具会把这两个数组原样写成 io_schema.json（并补齐 label / required，outputs 的 type 默认 file），
     不会再额外生成别的结构。因此这里填的每个条目都必须满足下文「io_schema.json 的生成规则」：
     `name` 必填且唯一、`type` 取组件类型表里的值、`label` 与 `description` 面向人类用户可读。
   - `io_schema_path` 是参数契约的唯一来源：后续 request_param 的键、params.json 的键、
     脚本内读取的参数名，全部以它为准，不要自行发明参数名。

2) **先确认契约**：结合用户需求与 `get_file` 拿到的数据结构（列名 / 维度 / 分组字段），
   把 io_schema.json 的 inputs / outputs 改成真实契约。
   - inputs 的 `name` 就是 params.json 里的键，也是脚本里取参数用的名字；
   - 输出项按实际产物命名（通常 type 为 file，表格类给 role=TABLE）；
   - io_schema.json 也用于给人类用户渲染表单，label / description 要写清楚含义；
   - 需要新增 / 调整参数时用 `update_script`（`file=io_schema` + `content` 整体覆盖，或 `old_string` / `new_string` 唯一替换）；
     查看当前正文用 `get_script`（`files=["io_schema"]`），**不要用 view / write / edit / bash 直接改路径**（规则见「流程约束」）。

3) **先摸清脚本真正读哪些参数键**（写脚本 / 建节点之前，一次读取代替后面反复试错）：
   - 用 `get_script`（`files=["script_file"]`）直接拿到脚本正文，检查 `params$`（qmd / R）或 `params[`（python）的读取键；
   - 脚本实际读取的键才是 params.json 必须提供的键。io_schema 的 inputs 只负责表单渲染，
     既有脚本常常还读 `params$analysis_name` 这类 **io_schema 未声明的扁平键**；
   - 这些未声明键**同样要写进 request_param**（后端把它们当未知键原样透传进 params.json，只给 warning），
     不要等脚本报 `argument is of length zero` 才发现键缺失。

4) **写脚本主体**：用 `update_script`（`file=script_file` + `content` 整体覆盖，或 `old_string` / `new_string` 局部替换）写入脚本文件，无需再走 write / bash：
   - 从 params.json 读取输入，键名以第 3 步读到的结果为准（io_schema 声明的名字必须对得上）；
   - 产物写入 params.json 中的 `output_dir`，命名与 io_schema outputs 对应；
   - 不要硬编码路径、样本名或分组，一律通过参数传入；
   - 具体写法见第四节「脚本代码怎么写」。

5) **脚本依赖直接使用，报错后再安装（见第六节）**：脚本正文写好后**不要提前探测、不要提前安装**依赖，
   代码里直接按最简写法 `library(ggplot2)` / `import pandas` / 调 `bwa`，然后直接建节点运行：
   - 运行成功 → 说明依赖齐全，无需任何安装动作；
   - 只有运行日志报错提示某个 R 包 / Python 包 / 生信软件不存在时，才用 `terminal_exec` 检查并尝试安装：
     先确认基础容器里有没有对应运行时（`Rscript` / `python` / `conda`），**有**才装到系统指定的包目录
     （`container_template_id` 就是脚本的 `container_template_id`）；**没有就停下来告知用户**，
     不要尝试安装运行时本身；
   - 安装遇到网络失败、缺系统库等情况，先重试一次，仍不行立即反馈用户，不要自己改目录或换源折腾。

6) **写 README.md（创建与修改脚本时都必须做）**：用 `update_script`（`file=readme`）写入脚本目录的
   README.md，按第三节「README.md：request_param 规范」的固定模板写：脚本用途 → 输入数据要求 →
   参数说明表（request_param 键 / 类型 / 说明）→ **可直接复制的 request_param 示例** → 输出产物 →
   注意事项。
   - `create_script` 时也可以直接传 `readme` 一次写好；后续脚本有改动（新增 / 改名参数、改输出、改数据要求）
     **必须同步更新 README**，并在写完代码、建节点之前完成，不要留到最后补。
   - README 必须反映**当前**的 io_schema 与脚本实际读取的 `params$` 键，包括未声明的扁平键；
     这是后续 agent 走 `data-file-analysis` 技能时唯一依赖的「使用方法」。

7) **建节点 = 一次 `create_analysis_node_script`（run=true）**，这是默认路径，一步到底：
   - 传入 `script_id` + `node_name` + **完整的 request_param**（= 第 3 步摸到的全部键，含未声明的扁平键）；
   - 默认 run=true：建好即跑完，返回 status / success / log，以及 `workspace_dir` / `params_path` /
     `log_path` / `script_file_path` / `io_schema_path`，直接拿来用，不必再去磁盘找路径；
   - **不要为了「先看 params 再决定」而传 run=false，再补 `build_analysis_node_params` + `run_analysis_node`**：
     带 request_param 时写入的参数与 `build_analysis_node_params` 完全一致，想核对直接读返回的
     `params_path`，确实要改才用 `build_analysis_node_params` 覆盖；
   - 只有用户明确要求「先别跑」时才传 run=false。

8) `build_analysis_node_params`：**仅用于「节点已存在、要改参数」**——按 io_schema inputs 的 name
   组织 request_param，覆盖节点已有参数并重写 params.json。返回 warnings 时先确认是不是第 3 步摸到的
   未声明扁平键：属实的未声明键可以保留（它们确实要进 params.json）。

9) `run_analysis_node` 运行并查看日志；失败时必须继续迭代：读日志定位原因 → 改脚本或改参数
   → 参数有变化才重新 `build_analysis_node_params` → 再次 `run_analysis_node`，
   直到返回 success=true 才算完成本次任务。若日志显示缺少 R 包 / Python 包 / conda 软件，回到第六节
   判断“容器里有没有对应运行时”，而不是无限重试。

10) **收尾前做 README 一致性检查**：脚本最终能跑通后，核对 README 的参数表 / 示例 request_param /
   产物清单与最终 io_schema、脚本实际读取的键、outputs.json 键名是否完全一致；不一致就再改 README。
   README 里必须能直接复制出一份**真的能跑通**的 request_param 示例。

### 流程约束

- **无依赖的工具调用必须同一轮合并发出**：`list_project_scripts` + `get_file` + 读文件头几行、
  `list_container_templates` + `ask_user`（选择容器模板时）、
  `get_script` + 读 io_schema.json + `grep params$` 脚本，这几组内部互不依赖，
  应在同一轮里并行调用，不要按上面的编号串行等待（编号是逻辑顺序，不是调用批次）。
- **创建脚本前必须锁定 container_template_id**：用户没指定时先 `list_container_templates` 再用 `ask_user` 让用户选，
  不得自己填默认模板或猜一个 ID；这个 ID 同时决定第六节依赖安装的位置与运行时能力。
- 任务未完成的标准只有一个：`run_analysis_node` 返回 success=true 且产物符合 io_schema outputs。
  不得在脚本尚未跑通时结束任务，也不得把「脚本已写好」「节点已创建」当作完成。
- **安装依赖只用 `terminal_exec`、只装到系统指定的包目录**：R 包 / Python 包 / conda 生信软件的安装目录
  已由系统通过环境变量指定并创建好，直接按第六节的命令安装，不传 `--library`、不改 `CONDA_ENVS_PATH` 之类目录参数。
- **容器里没有运行时就不装**：`Rscript` / `python` / `conda` 命令不存在时不要用 apt / conda 自己装运行时
  （装进容器也会随容器重启丢失），直接告诉用户「基础容器里没有该工具」，由用户换模板或自行处理（见第六节）；
  此时也不要继续盲调 `terminal_exec` 装包——工具不存在，装包命令只会报错。
- **先直接跑、报错再探测**：脚本依赖直接使用，不提前 `Rscript -e` / `import` 试探、不提前装包；
  只有运行报错提示包或软件不存在时，才用 `command -v Rscript python python3 conda` 一次看清运行时，
  再决定装哪种依赖、直接反馈还是停下；网络 / 系统库问题只重试一次，不自己换源改目录。
- **创建或修改脚本时必须同时创建 / 更新 README.md**：参数、输入要求、输出产物有任何变化都要同步；
  `create_script` 时通过 `readme` 参数给，之后用 `update_script`（`file=readme`）维护。
  只改脚本不更 README，会让后续 agent 按旧 README 组出错误的 request_param。
- 修改 README 用 `update_script`（`file=readme`），与改脚本 / io_schema 一样**不需要重建 script 或节点**，
  也不会触发运行；它是纯文档更新，但必须在收尾前完成。
- `create_script` 只建脚本；**它返回结果里没有 analysis_node_id**，不要把它当作已可运行的节点。
- 修改 io_schema.json 的 inputs / outputs 后**无需重新建 script 或节点**：`build_analysis_node_params`
  与 `run_analysis_node` 每次都会读取最新的 io_schema.json，并刷新节点的 patterns / resolved_inputs /
  request_param。只需按新契约重新调用 `build_analysis_node_params`，再运行即可。
- 修改脚本内容用 `update_script`（`file=script_file`），同样**无需重建 script 或节点**，直接调用 `run_analysis_node`
  即可运行最新代码；只有参数也要变化时才再调用 `build_analysis_node_params`。
- **改 request_param 用 `build_analysis_node_params`，改 node_name 用 `update_analysis_node_script`
  （analysis_node_id + node_name，默认更新后立即运行，只想改名时传 run=false），都不需要重建节点。**
- 需要在同一脚本上做多次独立分析时，用 `create_analysis_node_script`（script_id + node_name + 可选
  request_param）再建一个节点，各自持有自己的参数与工作目录，互不覆盖。
- `run_analysis_node` 不接受 request_param：脚本型节点直接使用节点上已保存的 request_param
  （由 `build_analysis_node_params` / `create_analysis_node_script` 写入），运行前按最新 io_schema 重新解析，
  并刷新 inputs_patterns / output_patterns / resolved_inputs / request_param、清空上一轮校验错误；
  命令型节点（`create_analysis_node_agent` 创建）直接运行自身 run.sh。
- `run_analysis_node` 与 `build_analysis_node_params` 职责不同：前者只负责运行，后者负责**修改既有节点的参数**。
  用 `create_analysis_node_script`（带 request_param、run=true）建节点时参数已经写入，**这是默认路径，不需要先 build**；
  只有节点已存在、要换参数时才用 `build_analysis_node_params`。
- 每次运行只改必要的地方：先用日志定位失败原因，避免整段重写脚本。
- 需要完整日志时读取 `run_analysis_node` 返回的 `log_path`。
- 若失败原因在可用信息范围内无法确定（需求歧义、缺少必要数据 / 参数 / 环境、多种方案取舍、
  create_script 报错提示需要用户配置容器模板等），调用 `ask_user` 提问，并在拿到答复后继续执行流程，
  不要在等待用户输入时提前收尾。
- `ask_user` 使用要点：`questions[].id` 用稳定英文短标识（如 analysis_type、group_column）；
  选项类问题用 single_choice / multiple_choice 并给出 2-5 个 option（label + value），
  必要时设 `allow_custom=true`；纯信息采集用 text 并给 `placeholder`；
  能一次问清的问题合并到同一次 `ask_user` 调用中，避免反复打断用户。

### 生物分组不明确时必须先 ask_user

分组是所有下游分析（差异分析、聚类、富集、生存分析等）的前提，猜错会让整条结果链失效。
因此在写脚本 / 组参数之前**先判断分组是否明确**；不明确时**必须先调用 ask_user**，
拿到答复后再继续，不得凭经验假设。

判断为「分组不明确」的典型情形（满足任一即需要提问）：

- 数据里存在多个候选分组列（如同时有 `group` / `condition` / `timepoint` / `cluster` / `celltype`），
  无法确定该用哪一列；
- 分组取值是抽象标签或编码（`Group1/Group2`、`A/B/C`、`0/1/2`、`cluster0/cluster1`），
  看不出哪组是对照、哪组是处理，也看不出分组的生物学含义；
- 用户需求涉及「实验组 / 对照组」「处理 / 未处理」「疾病 / 正常」「不同时间点或剂量」等对比，
  但数据中找不到对应字段或字段取值对应不上；
- 样本名里隐含分组（如 `Ctrl_1`、`KO_2`、`treat_3`），需要从样本名解析才能得到分组；
- 只有单个分组列但存在空值 / 拼写不一致 / 大小写混用（`WT`、`wt`、`WT_`），或分组数量与用户描述不符；
- 有多个分组维度同时存在，需要确认做比较的是哪一个（主分组列），其它维度是否作为协变量 / 批次。

提问要求：

- 提问前先用 `get_file` / 查看数据结构，把**真实的候选列名与取值**写进问题里，
  不要让用户从零描述（例如：「检测到 `group` 列取值为 `A/B`，哪一组是对照组？」）；
- `questions[].id` 使用稳定英文短标识，如 `group_column`、`group_levels`、`control_group`、`batch_column`；
- 选项类问题用 single_choice / multiple_choice，option 用「label（人类可读）+ value（真实列名 / 取值）」，
  候选分组列直接作为 option 列出，必要时设 `allow_custom=true` 让用户补充；
- 样本名解析场景用 text，给 `placeholder` 说明期望格式（如 `Ctrl_1 → Control`）；
- 分组列、对照 / 处理组、是否多批次纠正等**能一次问清的问题合并到同一次 ask_user 调用**，不要反复打断；
- 拿到答复后：把分组列作为 `BaseSelect`（或固定取值时用 `BaseSelect` + `data`）写进 io_schema inputs，
  脚本里通过 `params$<分组参数名>` 读取，不硬编码分组取值；若用户答复后仍有关键歧义，可再问一次，
  但不要跳过提问直接假设。

---

## 二、io_schema.json 的生成规则

`io_schema_path` 就是脚本目录下的 io_schema.json，是「脚本参数契约」的唯一来源，它同时决定三件事：

1. `inputs[].name` = `build_analysis_node_params` 里 `request_param` 的键（运行节点时使用节点已保存的参数）；
2. `inputs[].name` = params.json 的键，也就是脚本里 `params$<name>` 读取的参数名；
3. `inputs[]` 还会被渲染成人类用户在节点面板里填写的表单控件（由 `type` 决定控件形态）。

因此：**`request_param` 的键必须与 `inputs[].name` 逐一对应**（多写的键会被忽略并给出 warning，
少写必填项也会给出 warning）。不要自行发明参数名，也不要省略 `name`。

### 文件结构

~~~json
{
  "inputs": [
    {
      "name": "p_threshold",
      "label": "P value threshold",
      "type": "BaseInputNumber",
      "required": true,
      "description": "差异分析的显著性阈值（表单上展示给用户）"
    }
  ],
  "outputs": [
    { "name": "tsv", "type": "file", "role": "TABLE" }
  ],
  "params": [],
  "resources": { "cpu": 4, "memory": "6GB" }
}
~~~

- `inputs[]`：每个条目 `name` 必填且唯一；`label`（表单展示名）、`required`（默认 true）、
  `description`（表单说明）、`type`（控件类型，见下表）按需给出。
- `outputs[]`：每个条目 `name` 必填，`type` 通常为 `file`（也支持 `dir` / `table`），
  `role` 是该产物在 `analysis_result` 中发布用的键，默认 `DEFAULT`；
  **表格类产物必须写 `role: "TABLE"`，下游 FileSelect 才选得到**。
- `params` 一般保持 `[]`；`resources` 是运行资源，保持默认即可。

### inputs 支持的组件类型

| `type` | 表单控件 | `request_param` 值 | 脚本里怎么用 |
| --- | --- | --- | --- |
| `BaseInput` | 单行文本 | 字符串，如 `"FDR"` | `params$name` 为字符串 |
| `BaseTextArea` | 多行文本 | 字符串，可含换行 | `params$name` 为字符串 |
| `BaseTextAreaNum` | 多行文本 + 「共 N 项」提示 | 逗号分隔的字符串，如 `"CD4,CD8,FOXP3"` | `strsplit(params$name, ",")` |
| `BaseInputNumber` | 数字输入框 | JSON 数字，如 `0.05`（不要写成字符串） | `params$name` 为数值 |
| `BaseSwitch` | 开关 | JSON 布尔，如 `true` | `isTRUE(params$name)` |
| `BaseSelect` | 下拉框 | 选中的单个字符串值（多选为字符串数组） | `params$name` 为字符串 |
| `BaseColorPicker` | 颜色选择器 | 十六进制颜色字符串，如 `"#FF0000"` | 直接用于绘图颜色 |
| `FileSelect` | 上游结果文件 + 列选择器（见下节） | **对象**：`{"file": "<file id>", "<列名>": "<列名>"}` | `params$name$path` 为文件路径 |
| `Divider` | 分区标题（纯布局） | **不参与 request_param** | 不产生参数，不要读取 |

补充约定：

- 新建脚本**优先只使用上表列出的类型**；更细的字段写法（BaseSelect 的 `data` / `dataKey`、ThreeColorPicker 等）
  见 `analysis-io-schema-spec`。
- 项目里既有脚本已经在用复合类型（`GroupSelectAssayButton` 等，通常配合 `workflow` / `groups` / `colors`
  等顶层键）。**沿用既有 io_schema 时原样保留，不要改成上表的简单类型**：这类条目不产生 params 键，
  只决定样本 / 分组选择面板（详见 `analysis-io-schema-spec` 第 3.1 节）。
- `BaseSelect` 的候选项由数据源提供（`dataKey` / `resolver.accept_formats`）。固定取值（枚举）时，
  把候选值写在条目的 `data` 里（`[{ "label": "...", "value": "..." }]` 或字符串数组），
  `request_param` 里填这些 `value` 之一；不要填展示用的 label。
- `BaseTextAreaNum` 的值是「逗号分隔的字符串」，不是数组；`request_param` 也必须给字符串。
- `BaseInputNumber` / `BaseSwitch` 请给原生 JSON 数字 / 布尔；不要给 `"0.05"` / `"true"` 这类字符串。
- `Divider` 只用于在表单上分组，不要给它 `required`，也不要把它当参数：它不会出现在 `request_param` 中，
  脚本里也不要读取它。

### FileSelect：从上游结果表里选文件 + 选列

`FileSelect` 用于「当前脚本读某个上游节点产出的表格文件，并指定读其中哪几列」，
它是最常用的下游输入控件，`request_param` 的形态是**对象**，不是字符串。

#### io_schema 里怎么写

~~~json
{
  "name": "exp",
  "label": "Expression matrix",
  "type": "FileSelect",
  "required": true,
  "input_type": "file",
  "resolver": { "accept_formats": ["TABLE"] },
  "columns": ["sample_column", "gene_column", "value_column"],
  "modes": [0, 0, 0],
  "columns_rules": [1, 1, 1],
  "groups": [0, 0, 0]
}
~~~

各字段作用（`columns` / `modes` / `columns_rules` / `groups` 是**按下标一一对应**的数组）：

| 字段 | 含义 |
| --- | --- |
| `name` | 该输入在 `request_param` 里的键，也是 params.json 的键 |
| `type` | 固定 `"FileSelect"` |
| `label` | 表单上「选文件」下拉框的前缀名（渲染为 `<label> File`） |
| `input_type` | 固定 `"file"`（走文件解析分支，`resolved_inputs` 里会带上路径等文件字段） |
| `resolver.accept_formats` | 该输入接受的上游产物角色，通常 `["TABLE"]`；**上游 outputs 的 `role` 必须是 `TABLE`** |
| `columns` | 需要在选中文件里挑选的列，每一项渲染成一个「`<列名>` Columns」下拉框 |
| `modes[i]` | `1` 表示第 i 列可多选（多选值为数组），`0` 表示单选 |
| `columns_rules[i]` | `1` 表示第 i 列必填 |
| `groups[i]` | `1` 表示第 i 列额外渲染一个「`<列名>` Group Name」输入框（分组名） |

`modes` / `columns_rules` / `groups` 可以省略数组项，缺省按 `0` 处理。若只选文件、不需要指定列，
`columns` 也可以不写，此时表单只渲染「选文件」下拉框 + Open / List 按钮。

#### request_param 里怎么填

值是一个**对象**，不是字符串，也不是文件 ID：

~~~json
{
  "exp": {
    "file": "123",
    "sample_column": "SampleID",
    "gene_column": "GeneID",
    "value_column": "TPM"
  }
}
~~~

- `file`：上游结果文件的 **file id（字符串）**，对应 `<name> File` 下拉框的选中值；
- 每个 `columns[i]`：该列选中的列名（字符串）；`modes[i]=1` 时可给字符串数组；
- 若 `groups[i]=1`，对应 group 名由表单自动生成 `node_name`，一般**不需要**在 `request_param` 里手填。

解析后（`resolved_inputs.<name>`）会得到文件记录字段（`id` / `analysis_result_id` / `file_name` /
`path` / `format` / `size` / `md5` 等）+ 你填的每一列 + `form_type: "FileSelect"` + `groups`（即 `columns` 列表）。
因此脚本里 `params$exp` 是一个**对象**：文件路径在 `params$exp$path`，列名在
`params$exp$sample_column` 之类的位置，而不是在 `params$exp` 这个字符串上。

~~~r
# params$exp 是一个 list：文件信息 + 每一列选中的列名
exp_path   <- params$exp$path
sample_col <- params$exp$sample_column
gene_col   <- params$exp$gene_column
value_col  <- params$exp$value_column

exp <- read.delim(exp_path, check.names = FALSE)
~~~

#### 本脚本若要给下游用，必须产出 role = TABLE

~~~json
{
  "outputs": [
    { "name": "tsv", "type": "file", "role": "TABLE" }
  ]
}
~~~

- 只写 `type: "file"` 时 `role` 默认是 `DEFAULT`，下游 `accept_formats: ["TABLE"]` 选不到；
- 产物需是可被解析列的表格（tsv / csv / xlsx），路径按 `outputs[].name` 写进 outputs.json；
- 若下游还要「按列取值」，保持首行为列名，列名不要加引号或特殊字符。

### request_param 生成示例

~~~json
{
  "p_threshold": 0.05,
  "method": "BH",
  "features": "CD4,CD8,FOXP3",
  "use_filter": true,
  "plot_color": "#FF0000"
}
~~~

生成后：

- params.json 里会出现与上表同名的键（脚本按这些名字读取）；
- 若 `request_param` 出现 `inputs` 里没有的键，会被忽略并返回 warning，必须回到本节修正名字；
- 若必填项缺失，也会返回 warning，补齐后再运行。

> 这份键清单**不是只给自己用**：脚本创建 / 修改完成后，要把同一份清单写成 README.md 的参数表与
> 可复制示例（见第三节），后续 agent 才会按正确键名组织 request_param。

---

## 三、README.md：request_param 规范

README.md 是脚本的「使用方法」文档，存在脚本目录下（`get_script` 返回的 `readme_file`，
`readme_content` 就是它的正文）。后续 agent 在 `data-file-analysis` 技能里**优先只读这份 README**
就组织 request_param 调 `create_analysis_node_script`，因此它必须满足：

- 读完就能写出**完整、可直接复制运行**的 request_param；
- 参数表覆盖 io_schema 的全部 inputs **以及脚本额外读取的扁平键**；
- 输入数据要求写清「必须是哪类表格、必须有哪些列」，让 agent 一眼判断当前数据能不能用；
- 产物清单与 `outputs[].name` 一一对应，下游按 role 引用时才不会选错。

### 何时必须写 / 更新 README

- `create_script` 创建新脚本时 → 必须写（用 `readme` 参数，或创建后立刻 `update_script`（`file=readme`））；
- 修改脚本时 → 只要发生下列任一变化就必须同步更新 README：
  - 新增 / 删除 / 重命名 io_schema inputs 或 outputs；
  - 脚本里新读取或不再读取某个 `params$` 键（含未声明的扁平键）；
  - 输入数据的列要求、格式要求变化；
  - 产物的文件名规则、列结构、outputs.json 键名变化；
  - 参数取值约束变化（如某参数不可为空、枚举值改了）。

### 固定模板（按此结构写，标题与顺序保持一致）

~~~markdown
# <脚本名称：一句话说清做什么分析>

<2-4 句摘要：输入什么、比较什么、输出什么关键产物。>

## 一、输入数据要求

| 输入 | 格式 | 要求 |
| --- | --- | --- |
| `<输入参数名>`（表达矩阵） | TSV / XLS 表格，首行表头 | 必须含**基因标识列**（如 `gene_id`）与**每个样本一列**的原始 count；样本列名即 `control` / `treatment` 要填的值。 |
| `<可选输入参数名>` | TSV | 说明用途与缺省行为（不提供时结果如何）。 |

示例（本项目 <文件名> 前几列）：

```
gene_id   SP1    SP2   ...  gene_name  ...
```

## 二、参数说明

| 参数（request_param 键） | 类型 | 说明 |
| --- | --- | --- |
| `count.file` | 字符串 | 表达矩阵的**文件 ID**（不是路径），填 `@file:<id>` 冒号后的数字；脚本从 `params$count$path` 读路径。 |
| `count.feature` | 字符串 | count 表里作为基因标识的真实列名（如 `gene_id` / `gene_name`）。 |
| `count.control` | 字符串数组 | 对照组样本名，必须是 count 表里的真实列名。 |
| `analysis_name` | 字符串 | 产物文件名前缀。**不可为 null / 空串**。 |

### 参数示例（可直接复制运行）

```json
{
  "count": {
    "file": "2121084345698488320",
    "feature": "gene_name",
    "control": ["SA1", "SA2", "SA3"],
    "treatment": ["CF1", "CF2", "CF3"]
  },
  "gene_sig_type": "padj",
  "gene_sig_threshold": 0.05,
  "analysis_name": "CF_vs_SA"
}
```

> 关键提醒：<列出最容易漏的扁平键 / 不可为空参数 / 类型坑，一段话或三条以内。>

## 三、输出产物

| 产物 | 契约名 | 内容 |
| --- | --- | --- |
| `<analysis_name>_deg.tsv` | `deg` | 全部基因差异结果，列：... |
| `<analysis_name>_sig_deg.tsv` | `sig_deg` | 仅显著差异基因。 |
| `outputs.json` | — | 上述表格的登记信息，供下游节点按契约名引用。 |

## 四、运行方式

### 1. 在节点里按表单跑（推荐）

在项目里选本脚本建分析节点，按表单填 ... 然后运行。

### 2. 用脚本 ID 直接建节点（Agent 调用方式）

```
create_analysis_node_script(
  script_id: "<脚本 ID>",
  node_name: "<输入文件名去扩展名>_<分析命名>",
  request_param: <上面的参数示例>,
  run: true
)
```

同一套数据做多组比较时，对每个比较各建一个节点、各给一个 `analysis_name`，产物文件名互不覆盖。

## 五、注意事项

- <数据前提：如 count 必须是原始整数、每组至少 2 个样本、样本名必须与列名完全一致。>
- <常见报错与排查：如 `argument is of length zero` → 检查某几个扁平键是否齐全非空。>
~~~

### README 中 request_param 的书写规则

参数示例是 README 里最重要的一段，必须按以下规则写，示例要能被直接复制：

1. **键名 = io_schema `inputs[].name`**，再叠加脚本实际读取但 io_schema 未声明的扁平键
   （如 `gene_sig_type` / `analysis_name`），一个都不要漏；`Divider` 不产生参数，不要写进示例。
2. **示例必须是完整的一份**：所有必填项都给真实值，不要写 `<待填>` 之类占位符；
   可选输入（如 `anno`）在说明里写「不提供则如何」，在示例里可以整段省略。
3. **FileSelect 输入写对象**，`file` 填文件 ID 字符串，其余键为列名：

   ~~~json
   { "count": { "file": "2121084345698488320", "feature": "gene_name",
                "control": ["SA1", "SA2"], "treatment": ["CF1", "CF2"] } }
   ~~~

   并在参数表里写明「脚本从 `params$count$path` 读路径」，避免 agent 误传文件路径。
4. **类型必须与控件一致**：`BaseInputNumber` 给 JSON 数字（`0.05`，不是 `"0.05"`）、
   `BaseSwitch` 给布尔、`BaseTextAreaNum` 给逗号分隔字符串、`BaseSelect` 填 `value` 而非 `label`。
5. **样本 / 分组类参数要在示例里给出真实列名形式**（如 `["SA1","SA2","SA3"]`），
   并在参数表说明「必须是输入表里的真实列名，区分大小写与空格」。
6. **命名类参数（如 `analysis_name`）必须写明非空要求与产物命名影响**：留空会导致产物文件名为空而报错。
7. **参数表逐行对应 io_schema**：类型一列写「字符串 / 数字 / 布尔 / 字符串数组 / 对象（FileSelect）」，
   说明里写清默认值、取值范围、与表单 label 的对应关系（方便人类用户按表单填写）。
8. **不要在 README 里写死某次分析的样本名 / 文件 ID 作为唯一取值**：示例可以给真实值便于复制，
   但参数表必须说明这些值要按当前数据替换。

### 校验：README 与契约一致

写完后逐条对一遍，任何一条不一致都要改 README：

- io_schema `inputs[].name` 集合 ⊆ README 参数表的键集合 ∪ 脚本 `params$` 键集合；
- README 参数表里的每个键，脚本里确实有对应的 `params$<key>` 读取（或说明为表单布局键）；
- README 参数示例能通过 `build_analysis_node_params` 且无 `not declared in script io_schema inputs` /
  `missing required io_schema input(s)` 之外的新增 warning；
- README 产物契约名与 io_schema `outputs[].name`、脚本写出的 outputs.json 键名完全一致。

---

## 四、脚本代码怎么写

用 `get_script`（`files=["script_file"]`）拿到脚本正文与 params.json 中的内容，根据用户要求完成代码撰写：
**使用最简单的代码，尽量不使用函数**；图片直接在 qmd 中打印；不要读取其他文件，
只需要用 `update_script`（`file=script_file`）修改脚本正文。

- 读取 tsv / csv 时使用 tidyverse；代码只需简洁清楚。脚本要用的 R 包 / Python 包 / conda 生信软件
  **直接写 `library(...)` / `import ...` / 调用软件名即可**，不要提前探测或装包：
  运行报错提示依赖不存在时，才按第六节「软件安装规范」用 `terminal_exec` 检查并安装
  （容器里没有 `Rscript` / `python` / `conda` 这类运行时就不要尝试安装，立刻告知用户）。
- qmd 中打印表格需要使用 `knitr::kable()`。
- 路径一律由参数拼出（`params$output_dir`、`params$<FileSelect>$path`），不硬编码路径 / 样本名 / 分组。
- 不引入多余抽象：不加参数校验层、异常包装层、日志框架、命令行框架；读出来直接用。
- 最后一步总是写 outputs.json（见本节「输出约定」）。

### R 代码编写规范：能用解析器 + tidyverse，就不要手写解析

写 R 脚本时按下面的规范，目标是一眼看懂、列名干净、少踩坑。

1. **解析结构化格式优先用专用 R 包，不要手工 `readLines` / `strsplit` 切分**：

   | 格式 | 用什么 | 备注 |
   | --- | --- | --- |
   | GFF / GFF3 | `rtracklayer::import(path, format = "gff3")` → `as.data.frame()` | 自动跳过 `#` 注释行、**遇 `##FASTA` 停止**；手工切分会把 FASTA 序列行当成特征 |
   | BED / bigWig / chain | `rtracklayer::import()` | 同上，返回 GRanges |
   | VCF | `vcfR::read.vcfR()`（容器里有时才用） | 先按报错再装包，不提前假设 |
   | tsv / csv / txt | `readr::read_tsv()` / `read_csv()` | 固定列类型、自动去 BOM |
   | xlsx | `readxl::read_excel()` | |
   | 生信软件的制表符输出（eggNOG、BLAST 等） | `readr::read_tsv(comment = "##")` + 参数归一化表头 | 见第 4 条 |

   真实教训：同一份 Prokka GFF，手工 `readLines` + `str_split_fixed` 得到 41863 行「特征」，
   其中 37340 行其实是 `##FASTA` 之后的序列；`rtracklayer` 解析只有 4523 行真实特征。

2. **参数与读表就三行，不要包函数**：

   ~~~r
   params <- jsonlite::fromJSON("params.json")
   gff    <- as.data.frame(import(params$gff$path, format = "gff3")) %>% rename(seqid = seqnames) %>% select(-width)
   anno   <- read_tsv(params$anno$path, comment = "##", show_col_types = FALSE)
   ~~~

3. **join 用 `left_join` + `by = setNames()` 直接指定两侧列名**，不要为了连接先造一个同名的临时 ID 列：

   ~~~r
   joined <- left_join(gff_features, anno, by = setNames(anno_query_col, join_key))
   ~~~

   命中统计也直接算，不需要临时列：`sum(joined[[join_key]] %in% anno[[anno_query_col]])`。

4. **列名归一化要显式做**：`readr` 会把表头首列的 `#` 去掉（eggNOG 的 `#query` → `query`），
   而参数里用户仍会写 `#query`。两侧都归一化再取列，否则永远匹配不上：

   ~~~r
   names(anno)    <- sub("^#+", "", names(anno))
   anno_query_col <- sub("^#+", "", anno_query_col)
   ~~~

5. **列表型列（多值属性）先压平成字符串**：`rtracklayer` 的 `Parent` / `db_xref` 等是 `CharacterList`，
   直接 `write_tsv` 会写成 `c("a", "b")`。统一压平：

   ~~~r
   for (col in names(gff)) {
     if (is.list(gff[[col]])) gff[[col]] <- vapply(gff[[col]], paste, character(1), collapse = ",")
   }
   ~~~

6. **宽长转换、筛选、派生列一律用 tidyverse 动词**，不写 `for` 循环拼 `data.frame`，
   也不用 `do.call(rbind, list)`：

   ~~~r
   attrs_long <- gff %>%
     select(row_id, seqid, type, start, end, all_of(attr_cols)) %>%
     pivot_longer(all_of(attr_cols), names_to = "key", values_to = "value") %>%
     filter(!is.na(value), value != "")
   ~~~

7. **核心列 / 属性列用变量区分**，只在产物里保留真正需要的列（去掉 `width`、临时 `row_id` 等）：

   ~~~r
   core_cols <- c("seqid", "source", "type", "start", "end", "score", "strand", "phase")
   attr_cols <- setdiff(names(gff), core_cols)
   ~~~

8. **写产物统一 `readr::write_tsv(x, path, na = "")`**（空值留空，不写 `NA` 字样）；
   需要保序、不去引号时不加 `quote = FALSE` 之类参数，`write_tsv` 默认就是无引号制表符分隔。

9. **报错信息带上可选值清单**，省掉下一轮猜名字：
   `stop("注释表里没有连接列: ", col, "（可用: ", paste(names(anno), collapse = ", "), "）")`。

10. **注释只在关键步骤写一行中文**（「读 GFF」「按 key 左连接」「写产物」），
    不要给每行代码配注释；参数名 / 变量名用英文且见名知义（`gff`、`anno`、`joined`、`attrs_long`）。

11. **整份脚本的控制流应当是从上到下平铺的 5~7 步**：读参数 → 解析输入 → 可选筛选 → 连接 → 写产物 + outputs.json。
    如果出现多层嵌套 `if` / 自定义函数 / 中间落盘再读回，先把它压回平铺写法。

### 输出约定：按 outputs 产出并写 outputs.json

#### 1. 先读 io_schema.json 拿到 outputs

- 调用 `get_script`（需要 script_id，`files=["io_schema"]`）直接拿到 io_schema.json 正文；
  节点上下文里已给出 io_schema.json 时也可直接查看。
- 只看 `outputs` 数组，逐项确认：
  - `name`：产物名，也是 outputs.json 的键名；
  - `type`：产物类型（通常为 file）；
  - `role`：用途（如 TABLE / FIGURE），决定产物格式倾向。
- `outputs` 为空数组时，脚本不需要写 outputs.json。

#### 2. 按 outputs 用 update_script 修改脚本

- `outputs` 有几项，脚本就要产出几项；产物路径一律拼在 `params$output_dir` 下。
- 产物格式与 `type` / `role` 对应：`role: TABLE` 用 tsv / csv，`role: FIGURE` 用 png / pdf。
- 产物写入文件即可；qmd 中需要展示的表格仍用 `knitr::kable()`。

单输出示例（`outputs[].name` 为 `tsv`）：

~~~r
outputs <- list(
  tsv = mutate_genes_path
)
jsonlite::write_json(outputs, file.path(params$output_dir, "outputs.json"), auto_unbox = TRUE, pretty = TRUE)
~~~

多输出时按 `name` 逐个补齐：

~~~r
outputs <- list(
  tsv = mutate_genes_path,
  plot = volcano_plot_path
)
jsonlite::write_json(outputs, file.path(params$output_dir, "outputs.json"), auto_unbox = TRUE, pretty = TRUE)
~~~

#### 3. 检查要点

- outputs.json 的键名必须与 `io_schema.outputs[].name` 完全一致（大小写、下划线都不能改），
  多写或少写都会导致产物校验失败。
- 值写实际产物路径（由 `params$output_dir` 拼出的路径），不要写相对路径。
- `outputs` 为空数组时不写 outputs.json。

> 代码与 README 必须一致：脚本实际读取的每个 `params$` 键、实际产出的每个文件，
> 都要对应到 README 的参数表 / 产物表；改完代码立刻回头同步 README（见第三节）。

---

## 五、运行与迭代

- 当用户说「运行当前节点 / 跑一下这个节点」时，调用 `run_analysis_node`，`analysis_node_id` 留空即运行
  当前上下文节点；用户给出其它节点 ID 时，把该 ID 原样传给 `analysis_node_id`。
- 工具会阻塞等待节点进入终态，返回 status / success / log；失败时先看 log 再判断是否需要改脚本重跑。
- **修改代码后总是需要调用 `run_analysis_node` 重新运行分析**，返回 success 才算完成；
  参数也变了时先 `build_analysis_node_params`。
- 只改必要的地方，不要整段重写；需要完整日志时读返回的 `log_path`。
- **跑通后仍要回看 README**：若本次调试中改了参数名 / 新增了扁平键 / 调整了产物，
  收尾前用 `update_script`（`file=readme`）同步，确保下次别人照 README 能一次跑通。

---

## 六、软件安装规范：R 包 / Python 包 / conda 生信软件

脚本时常需要基础容器里没有的包或软件（R 包、Python 包、`bwa` 这类生信软件）。
**默认直接使用**：代码里正常写 `library(ggplot2)` / `import pandas` / 调 `bwa`，不提前探测、不提前安装；
**只有运行报错提示某个包或软件不存在时**，才用 `terminal_exec` 在**脚本所属的容器模板**里检查并尝试安装，
并且**只装到系统已经指定的目录**。按下面的决策顺序判断，不要跳步、不要自己找目录。

### 1. 决策顺序（严格按此顺序判断）

1. **直接使用，不提前探测 / 安装**：脚本与 README 按最简写法正常引用依赖，直接建节点运行；
   基础容器里缺什么，会在运行时以「不存在」的报错暴露出来。

2. **只有运行报错提示依赖不存在时，才用 `terminal_exec` 检查并尝试安装**。典型报错：
   - R 包：`there is no package called 'ggplot2'` / `could not find function "xxx"`；
   - Python 包：`ModuleNotFoundError: No module named 'pandas'` / `ImportError: ...`；
   - 生信软件：`bwa: command not found` / `bwa: not found` / `exec: "bwa": executable file not found in $PATH`。

   确认依赖确实缺失后，再按下面第 3、4 步检查运行时并安装；报错与依赖无关（如列名不对、参数缺失）时
   不要去装包。

3. **安装前先确认基础容器里有没有对应运行时**（这是最关键的一步）：
   - `command -v Rscript`、`command -v python || command -v python3`、`command -v conda`；
   - **有哪个运行时，才允许装哪种依赖**：
     - 有 `Rscript` → 才能装 R 包；
     - 有 `python` / `python3` → 才能用 pip 装 Python 包；
     - 有 `conda` → 才能用 conda 装生信软件（`bwa`、`samtools`、`fastqc` 等）；
   - **容器里没有对应命令时，不要尝试安装它本身**（不要用 apt / apt-get / micromamba 引导安装 R、Python、conda）。
     这类运行时属于基础镜像，装进容器也只是临时的，容器重启就会丢。此时**直接向用户返回**：
     「该容器模板不存在脚本需要的 `Rscript` / `python` / `conda`，请换用安装了对应工具的容器模板，
     或自行在基础容器里安装」，然后停下来等用户处理，不要在容器里反复试错。

4. **确认运行时存在后，才用 `terminal_exec` 安装到系统指定目录**（见第 2 小节命令）。
   `container_template_id` 就是脚本的 `container_template_id`（`get_script` / `list_project_scripts` /
   `create_analysis_node_script` 返回的那个；新建脚本时就是第 0.1 步用户通过
   `list_container_templates` + `ask_user` 选定的那个），**不要用其它模板 ID 去装包**，
   否则包会装到别的容器目录，脚本依然找不到。

5. **安装失败先重试一次**：同一个命令重跑一次仍失败，立即把真实报错与控制台输出反馈给用户，
   让用户解决网络问题或自己装包；不要自行降级、换镜像源、改安装目录、切换版本继续折腾。

### 2. 安装目录由系统指定，不要改动

包安装目录由系统通过环境变量在容器创建时指定并已创建好
（`internal/manager/container_create_worker.go` / `runtime_variable.go` 里的
`R_PACKAGE_DIR`、`PYTHON_PACKAGE_DIR`、`CONDA_PACKAGE_DIR`，外加 `R_PROFILE`、`R_LIBS_USER` 等）：

| 依赖类型 | 环境变量 | 安装命令（直接照用，不要改目录参数） |
| --- | --- | --- |
| R 包 | `R_PACKAGE_DIR` | `Rscript -e 'install.packages("ggplot2", repos="https://cloud.r-project.org")'` |
| Python 包 | `PYTHON_PACKAGE_DIR` | `python -m pip install pandas`（有 `python3` 时用 `python3 -m pip`） |
| conda 生信软件 | `CONDA_PACKAGE_DIR` | **优先 mamba**：`mamba install -y -n bwa -c conda-forge -c bioconda bwa`；没有 mamba 时才 `conda install -y -n bwa -c conda-forge -c bioconda bwa` |

约束：

- **不要在安装命令里手写 `-l / --library`、`--target`、`--prefix`、`--user`、`-p / --prefix` 等目录参数**，
  也不要 `export R_LIBS_USER=...` / `export PYTHONPATH=...` / 换 conda 根目录：目录已经由系统设好，
  自己改会让包装到运行时找不到的地方，节点依然报「没有这个包」。
- 需要确认到底装到了哪里时，打印环境变量即可：`echo "$R_PACKAGE_DIR $PYTHON_PACKAGE_DIR $CONDA_PACKAGE_DIR"`，
  不要 `ls` / `find` 到处找或自己新建目录。
- 同一命令内可用 `&&` 串联多个包，一次性装完，比多次调用 `terminal_exec` 快；
  安装属于「一次性终端」，**不要为了装包传 `persistent=true`**，也不要在一条命令里 `export R_LIBS_USER=...`
  之后指望下次调用还生效（默认 `persistent=false`，命令结束容器即销毁，`cd` / `export` 状态不保留）。
- 包目录是**容器内路径**（对应宿主项目目录下的 `package/R-<template_id>`、`package/python-<template_id>`、
  `package/conda-<template_id>`），节点运行时用同一个环境变量，所以装在这里脚本一定找得到；
  在宿主机器上直接操作这些目录没有意义，一切安装都在容器内用 `terminal_exec` 完成。
- 代码里**不要**写死包目录（不要 `library(某包, lib.loc="/xxx")`、`sys.path.append("/xxx")`），
  一律依赖运行时已注入的环境变量；生信软件同理，**脚本里不写死软件安装路径**
  （不要 `export PATH=/…/envs/prokka/bin:$PATH`、也不要 `/…/prokka/bin/prokka` 这种绝对路径），
  一律用命令调用（见第 3 小节）。

### 3. 生信软件优先用 mamba 安装，调用一律用命令

- conda / mamba 在本项目里只负责装 `bwa`、`samtools`、`fastqc`、`star`、`prokka` 这类**生信软件**；
- **不要用 conda 装 R 或 Python 本身**，也不要用 conda 装 R 包 / Python 包（那会建出系统不认识的新环境）；
  R 包用 `install.packages()`，Python 包用 pip，各走各的目录。
- **安装优先用 mamba**：先 `command -v mamba`，有就用 `mamba install`（求解快、不易卡 resolver），
  没有再用 `conda install`。频道顺序固定 `-c conda-forge -c bioconda`。
- 软件装进**以软件名命名的环境**：`mamba install -y -n prokka -c conda-forge -c bioconda prokka`。
  环境目录由系统 `envs_dirs` 决定，**不要手写 `-p` / `--prefix` 绝对路径**，也不要 `conda activate`
  （一次性终端里 activate 不持久，节点运行也不会继承）。
- **调用已安装的软件一律走命令，不在脚本 / 命令节点里写死安装路径**：

  ~~~bash
  mamba run -n prokka prokka --version
  mamba run -n prokka prokka --outdir out --prefix demo genome.fsa
  ~~~

  - 有 mamba 用 `mamba run -n <env> <cmd>`；没有则用 `conda run -n <env> --no-capture-output <cmd>`
    （加 `--no-capture-output` 才能把软件日志实时打出来）；
  - **禁止**写 `export PATH=.../envs/prokka/bin:$PATH`、`/data2/.../package/conda-<id>/prokka/bin/prokka`
    这类绝对路径：换容器 / 换项目就失效，也违反「不硬编码路径」的约定；
  - 软件装在 `-p` 前缀目录（历史遗留）时同理用
    `conda run -p "$CONDA_PACKAGE_DIR/<软件名>" --no-capture-output <cmd>`，仍不写 bin 绝对路径。

### 4. 安装 R 包缺系统库：停下来反馈用户

R 包编译/安装失败、报错指向缺少系统库时（典型如 `libcurl`、`openssl`、`libxml2`、`libpng`、`libjpeg`、`libz`、
`gsl`，`configure: error: ... not found`、`/usr/bin/ld: cannot find -lxxx`、`fatal error: xxx.h: No such file`），
说明基础镜像缺 apt 系统依赖，**这不是能用 R 包安装命令解决的**：

- 不要自己 `apt-get install` 装系统库（容器里没有 root、也无法持久化）；
- **立即把缺的库名与报错原文反馈给用户**，请用户在基础容器（容器模板镜像）里安装对应系统库，
  或改用已包含该库的容器模板；
- 反馈后停下手上的安装，不要同一个包反复重试，也不要改脚本绕开这个包。

### 5. 何时反馈用户（归纳）

遇到下列任一情况，**立即反馈用户并停下**，不要自己想办法绕过：

- 容器里没有 `Rscript` / `python` / `conda`（即便装了也会随容器重启丢失）；
- 安装命令重试一次后仍失败（网络问题、镜像不可达、下载超时、证书错误）；
- R 包安装缺系统库，或报错指向镜像/编译器的系统依赖；
- conda 安装找不到包 / 频道不可达、或需要新建环境才能装。

反馈时写清：缺什么（包名 / 软件名）、在哪个 `container_template_id` 里试过、执行了什么命令、
真实报错关键几行，并给出可选方案（换模板 / 用户装包 / 用户修网络）。反馈后等用户答复，再继续流程。

尤其注意「换模板」这类需要用户拍板的事：容器没有运行时、或用户选定的模板缺依赖时，
**不要自己换一个模板重新建脚本 / 重装**，把现状说清楚后用 `ask_user` 让用户决定是否换模板。

尤其注意「换模板」这类需要用户拍板的事：容器没有运行时、或用户选定的模板缺依赖时，
**不要自己换一个模板重新建脚本 / 重装**，而是把现状说清楚，用 `ask_user` 让用户决定是否换模板。

### 6. 安装完的验证

装完后在同一次 `terminal_exec` 里顺手验证一次，确认包真的能被脚本找到：

```bash
Rscript -e 'library(ggplot2); cat("ok\n")'
python -c 'import pandas; print("ok")'
mamba run -n bwa bwa 2>&1 | head -n 1   # 无 mamba 时用 conda run -n bwa --no-capture-output bwa
```

验证通过后再重新 `run_analysis_node`（节点已建好，直接重跑即可）；验证不通过按第 5 小节反馈用户
（仍是容器环境问题，不是脚本问题）。

### 7. 在脚本里怎么用这些包

安装只是让包**在容器里可用**，脚本里依旧按最简写法 `library(ggplot2)` / `import pandas`：
不需要写 `install.packages()` / `pip install` 到脚本里（每次运行都会重新装，又慢又容易失败），
也不需要加路径参数。脚本里直接引用依赖、不提前探测；某个依赖报「不存在」时再照第六节的表格装一次。

生信软件在脚本 / 命令节点里一律用命令调用，不写死安装路径：

~~~bash
# 对：走 mamba run / conda run，脚本可移植
mamba run -n prokka prokka --outdir "$OD" --prefix demo --kingdom Bacteria --genus Ligilactobacillus "$IN"

# 错：写死环境里的 bin 路径，换容器 / 换项目即失效
export PATH="/…/package/conda-<id>/prokka/bin:$PATH"
/…/package/conda-<id>/prokka/bin/prokka --version
~~~

---

## 七、遇到重复性错误时写入 memory

工具调用「连续出现同一类错误」时**必须**把「如何避免再犯」写入长期记忆，避免后续轮次重复踩坑。

触发条件（满足任一即需要写入）：

- 同一个工具在同一任务内连续失败 2 次以上，且报错文本相同或高度相似；
- 同一个错误在**不同任务 / 不同会话**中重复出现（说明是稳定的坑，不是偶发）；
- create_script / create_analysis_node_script / build_analysis_node_params 报错提示需要用户配置环境
  （容器模板、路径、依赖）等无法由代码修好的问题；
- 依赖安装失败（容器没有 `Rscript` / `python` / `conda`、网络不可达、R 包缺系统库）并向用户反馈后，
  用户说明了原因或拍板了做法；
- 使用 ask_user 向用户确认后，用户明确纠正了某个参数格式 / 字段名 / 流程顺序。

写入方式：调用 `save_memory` 工具，`kind` 用 `note`，`content` 按以下结构写（简明扼要，一段话即可）：

    场景：<在做什么操作时触发>
    现象：<真实的报错文本或异常表现>
    原因：<根因，例如参数键名与 io_schema 不一致 / 忘了先 build_analysis_node_params>
    做法：<下次正确的操作顺序或参数写法>

注意：

- 只记录**可复用的规律**，不要把一次性的临时值（具体文件 ID、随机路径、某次的数据列名）写进记忆；
- 同一条注意事项再次出现时，用 `save_memory` 传 `memory_id` 更新原记忆，不要重复新建
  （可从系统提示词的「相关记忆」块中拿到已有记忆的 ID 与内容）；
- 记忆里写的是「避免错误的方法」，不要写与本次任务无关的对话摘要；
- 写入记忆后仍然要继续完成当前任务，不能用「已记录」代替修复问题。
