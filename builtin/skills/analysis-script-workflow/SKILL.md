---
name: analysis-script-workflow
description: 创建 / 修改生物信息学分析脚本并跑通分析节点的固定流程：create_script → 确认 io_schema 契约 → 写脚本 → create_script_analysis → build_analysis_node_params → run_analysis_node，包含 io_schema.json 生成规则、FileSelect 输入输出写法、生物分组不明确时必须先 ask_user 询问用户，以及失败迭代约束。用户要求新建分析脚本 / 分析节点、修改脚本或运行节点时使用。
user-invocable: true
---

## 创建脚本工作空间的固定流程

当用户要求「新建一个分析脚本 / 分析节点」并基于数据文件跑出结果时，必须按以下顺序推进，
并且**一次任务内走完整条流程**：从创建脚本开始，一直到 run_analysis_node 返回 success 为止。

0) 先判断分组是否明确：分组（实验组 / 对照组、处理组、疾病组等）是所有分析的前提。
   若无法从用户需求与数据结构中确定分组列与各组的生物学含义，**必须先调用 ask_user 向用户提问**，
   拿到答复后再进入第 1 步；不得先猜一个分组把流程跑完（详见「生物分组不明确时必须先 ask_user」）。

1) create_script → 只创建脚本，得到 script_id / script_file_path / io_schema_path（**不会创建分析节点**）。
   - 调用时一次性给出 inputs / outputs（真实的参数契约），避免创建后再补齐。
   - **create_script 的 `inputs` 就是 io_schema.json 的 `inputs`，`outputs` 就是 io_schema.json 的 `outputs`**：
     工具会把这两个数组原样写成 io_schema.json（并补齐 label / required，outputs 的 type 默认 file），
     不会再额外生成别的结构。因此这里填的每个条目，都必须满足下文「io_schema.json 的生成规则」：
     `name` 必填且唯一、`type` 取上表中的组件类型、`label` 与 `description` 面向人类用户可读。
   - io_schema_path 是参数契约的唯一来源：后续 request_param 的键、params.json 的键、
     脚本内读取的参数名，全部以它为准，不要自行发明参数名。
2) 先确认契约：结合用户需求与 read_file_by_id 拿到的数据结构（列名 / 维度 / 分组字段），
   把 io_schema.json 的 inputs / outputs 改成真实契约。
   - **在确定分组字段（biological grouping）之前，必须先判断分组信息是否明确**：
     若数据里存在多个可能作为分组的列、分组标签含义不明确（如 `Group1/Group2`、`A/B`、数字编码 `1/2`）、
     分组列缺失、或用户未说明哪个是实验组 / 对照组，**禁止自行猜测分组**，必须先调用 ask_user 询问用户，
     拿到明确答复后再继续（详见下文「生物分组不明确时必须先 ask_user」）。
   - inputs 的 name 就是 params.json 里的键，也是脚本里取参数用的名字；
   - 输出项按实际产物命名（通常 type 为 file）；
   - io_schema.json 也用于给人类用户渲染表单，因此 label / description 要写清楚含义；
   - 需要新增 / 调整参数时，直接编辑 io_schema_path 指向的这份 JSON，规则同上文（只使用表中的
     inputs 组件类型，Divider 只做分组、不产生参数）。
3) 再写脚本主体（写入 script_file_path）：
   - 从 params.json 读取输入，读取的键名必须与 io_schema inputs 的 name 完全一致；
   - 产物写入 params.json 中的 output_dir，命名与 io_schema outputs 对应；
   - 不要硬编码路径、样本名或分组，一律通过参数传入。
4) create_script_analysis（script_id = 第 1 步返回的 script_id，可给 node_name）：创建分析节点，
   得到 analysis_node_id / workspace_dir / params_path。
   - **默认 run=true：创建后立即运行该节点并等待结束**，返回 status / success / log / log_path。
     只想建节点、稍后自己控制运行时机时传 run=false。
   - 返回结果同时也包括 `script_file_path`（脚本主文件）、`io_schema_path`（脚本 io_schema.json）与
     `request_param`（节点当前保存的请求参数），无需再去 create_script 的结果或磁盘上找这些路径。
   - `request_param` 是可选输入：传入时按脚本 io_schema.json 解析并生成 params.json，其生成结果（params_path
     与 params.json 内容）与 build_analysis_node_params 完全一致。可用返回的 `params_path` 直接查看 params.json
     内容是否符合预期：不符合则调用 build_analysis_node_params 修改参数；符合则直接调用 run_analysis_node 运行。
   - 节点在创建时会按脚本 io_schema.json 初始化 inputs_patterns / output_patterns，但这只是初始值：
     **之后修改 io_schema.json 的 inputs / outputs 不需要重新建 script，也不需要重新建分析节点**，
     build_analysis_node_params 与 run_analysis_node 每次执行都会重新读取脚本最新的 io_schema.json，
     并按它刷新节点的 patterns / resolved_inputs / request_param。
   - 同理，**修改 script_file_path 指向的脚本内容后也无需重建 script / 节点**，直接调用 run_analysis_node
     （参数有变化时先 build_analysis_node_params）即可运行最新代码。
   - **后续修改节点的 request_param 只需调用 build_analysis_node_params，不需要重新建 script 或节点**；
     修改 node_name 则调用 update_script_analysis（入参 analysis_node_id + node_name）。
   - 需要同一脚本的多次独立分析时，对本步重复调用（每次一个节点，各自持有自己的参数与工作目录）。
5) build_analysis_node_params：按 io_schema inputs 的 name 组织 request_param（键 = name），
   把参数保存到分析节点并生成 params.json。若返回 warnings（缺必填项、存在 io_schema
   未声明的键），先修正再继续。
6) run_analysis_node 运行并查看日志；失败时必须继续迭代：读日志定位原因 → 改脚本或改参数
   → 重新 build_analysis_node_params（参数有变化时）→ 再次 run_analysis_node，
   直到返回 success=true 才算完成本次任务。

约束：
- 任务未完成的标准只有一个：run_analysis_node 返回 success=true 且产物符合 io_schema outputs。
  不得在脚本尚未跑通时结束任务，也不得把「脚本已写好」「节点已创建」当作完成。
- create_script 只建脚本；**它返回结果里没有 analysis_node_id**。不要把它当作已可运行的节点。
- 若失败原因在可用信息范围内无法确定（需求歧义、缺少必要数据/参数/环境、多种方案取舍、
  create_script 报错提示需要用户配置容器模板等），调用 ask_user 向用户提问，并在拿到答复后
  继续执行流程，不要在等待用户输入时提前收尾。
- **生物分组不明确时禁止自行假设**：只要无法从用户需求与数据结构中确定「用哪一列分组、各组代表什么、
  哪组是对照组」，就必须先调用 ask_user 提问（问题里写出真实候选列名与取值），拿到答复后再写脚本、
  组参数并运行；不得先跑一个猜测的分组结果再向用户确认。详见「生物分组不明确时必须先 ask_user」。
- ask_user 使用要点：questions[].id 用稳定的英文短标识（如 analysis_type、group_column）；
  选项类问题用 single_choice / multiple_choice 并给出 2-5 个 option（label + value），
  必要时设置 allow_custom=true；纯信息采集用 text 并给出 placeholder；
  能一次问清的问题合并到同一次 ask_user 调用中，避免反复打断用户。
- run_analysis_node 不接受 request_param：脚本型节点直接使用节点上已保存的 request_param（由 build_analysis_node_params / create_script_analysis 写入），运行前按最新 io_schema 重新解析，并刷新 inputs_patterns / output_patterns / resolved_inputs / request_param、清空上一轮校验错误；命令型节点（create_agent_analysis 创建）直接运行自身 run.sh。
- run_analysis_node 与 build_analysis_node_params 职责不同：build_analysis_node_params 负责修改参数，run_analysis_node 只负责运行。修改节点名称请调用 update_script_analysis（analysis_node_id + node_name 均必填），它会只改 node_name，不触碰参数与运行状态。**修改节点的 request_param 用 build_analysis_node_params，修改 node_name 用 update_script_analysis，都不需要重建节点**。
- 需要在同一脚本上做多次独立分析时，用 create_script_analysis（script_id + node_name + 可选 request_param）再建一个节点，各自持有自己的参数与工作目录，互不覆盖。
- create_script_analysis 的 request_param 是可选的，传入时生成的 params_path / params.json 与 build_analysis_node_params 的结果完全相同；默认创建后即运行（run=true），如只需建节点请传 run=false。
- 需要「先校验参数、确认 params.json 映射」时，调用 build_analysis_node_params；只是临时换一组参数跑一次时，也请先用 build_analysis_node_params 保存，再调用 run_analysis_node。
- 因此禁止在脚本型节点未调用 build_analysis_node_params（节点上无参数）的情况下直接运行节点。
- 修改 io_schema.json 的 inputs / outputs 后**无需重新建 script 或节点**：build_analysis_node_params 与 run_analysis_node 每次都会读取最新的 io_schema.json 并刷新节点的 patterns / resolved_inputs / request_param。只需按新契约重新调用 build_analysis_node_params，再运行即可。
- 修改 script_file_path 指向的脚本内容后同样**无需重建 script 或节点**，直接调用 run_analysis_node 即可运行最新代码；只有参数也需要变化时才再调用 build_analysis_node_params。
- 每次运行只改必要的地方：先用日志定位失败原因，避免整段重写脚本。
- 需要完整日志时读取 run_analysis_node 返回的 log_path。

### 生物分组不明确时必须先 ask_user

分组是所有下游分析（差异分析、聚类、富集、生存分析等）的前提，猜错会让整条结果链失效。
因此在写脚本 / 组参数之前，**先判断分组是否明确**；不明确时**必须先调用 ask_user 提问**，
拿到用户答复后再继续流程，不得凭经验假设。

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

- 提问前先用 `read_file_by_id` / 查看数据结构，把**真实的候选列名与取值**写进问题里，
  不要让用户从零描述（例如：「检测到 `group` 列取值为 `A/B`，哪一组是对照组？」）；
- `questions[].id` 使用稳定英文短标识，如 `group_column`、`group_levels`、`control_group`、`batch_column`；
- 选项类问题用 `single_choice` / `multiple_choice`，option 用「label（人类可读）+ value（真实列名 / 取值）」，
  候选分组列直接作为 option 列出，必要时设 `allow_custom=true` 让用户补充；
- 样本名解析场景用 `text`，给 `placeholder` 说明期望格式（如 `Ctrl_1 → Control`）；
- 分组列、对照 / 处理组、是否多批次纠正等**能一次问清的问题合并到同一次 ask_user 调用**，不要反复打断；
- 拿到答复后：把分组列作为 `BaseSelect`（或固定取值时用 `BaseSelect` + `data`）写进 io_schema inputs，
  脚本里通过 `params$<分组参数名>` 读取，不硬编码分组取值；若用户答复后仍有关键歧义，可再问一次，
  但不要跳过提问直接假设。

### 遇到重复性错误时写入 memory

工具调用「连续出现同一类错误」时，**必须**把「如何避免再犯」写入长期记忆，避免后续轮次重复踩坑：

触发条件（满足任一即需要写入）：
- 同一个工具在同一任务内连续失败 2 次以上，且报错文本相同或高度相似；
- 同一个错误在**不同任务 / 不同会话**中重复出现（说明是稳定的坑，不是偶发）；
- create_script / create_script_analysis / build_analysis_node_params 报错提示需要用户配置环境（容器模板、路径、依赖）等
  无法由代码修好的问题；
- 使用 ask_user 向用户确认后，用户明确纠正了某个参数格式 / 字段名 / 流程顺序。

写入方式：调用 `save_memory` 工具，`kind` 用 `note`，`content` 按以下结构写（简明扼要，一段话即可）：

    场景：<在做什么操作时触发>
    现象：<真实的报错文本或异常表现>
    原因：<根因，例如参数键名与 io_schema 不一致 / 忘了先 build_analysis_node_params>
    做法：<下次正确的操作顺序或参数写法>

注意：
- 只记录**可复用的规律**，不要把一次性的临时值（具体的文件 ID、随机路径、某次的数据列名）写进记忆；
- 同一条注意事项再次出现时，用 `save_memory` 传 `memory_id` 更新原记忆，不要重复新建（可从系统提示词的
  「相关记忆」块中拿到已有记忆的 ID 与内容）；
- 记忆里写的是「避免错误的方法」，不要写与本次任务无关的对话摘要；
- 写入记忆后仍然要继续完成当前任务，不能用「已记录」代替修复问题。

## io_schema.json 的生成规则

`io_schema_path` 就是 io_schema.json，是「脚本参数契约」的唯一来源，它同时决定三件事：

1. `inputs[].name` = build_analysis_node_params 里 `request_param` 的键（运行节点时使用节点已保存的参数）；
2. `inputs[].name` = params.json 的键，也就是脚本里 `params$<name>` 读取的参数名；
3. `inputs[]` 还会被渲染成人类用户在节点面板里填写的表单控件（由 `type` 决定控件形态）。

因此：**`request_param` 的键必须与 `inputs[].name` 逐一对应**（不能多写，多写的键会被忽略并给出 warning；
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

- `inputs[]`：每个条目 `name` 必填且唯一；`label`（表单展示名）、`required`（是否必填，默认 true）、
  `description`（表单说明）、`type`（控件类型，见下表）按需给出。
- `outputs[]`：每个条目 `name` 必填，`type` 通常为 `file`（也支持 `dir` / `table`），`role` 是该产物在
  `analysis_result` 中发布用的键，默认 `DEFAULT`，表格类产物写 `TABLE`（下游 FileSelect 才能按 TABLE 选到）。
- `params` 一般保持 `[]`；`resources` 是运行资源，保持默认即可。

### inputs 支持的组件类型（只使用下表）

| `type` | 表单控件 | `request_param` 值 | 脚本里怎么用 |
| --- | --- | --- | --- |
| `BaseInput` | 单行文本 | 字符串，如 `"FDR"` | `params$name` 为字符串 |
| `BaseTextArea` | 多行文本 | 字符串，可含换行 | `params$name` 为字符串 |
| `BaseTextAreaNum` | 多行文本 + 「共 N 项」提示 | 逗号分隔的字符串，如 `"CD4,CD8,FOXP3"` | `strsplit(params$name, ",")` |
| `BaseInputNumber` | 数字输入框 | JSON 数字，如 `0.05`（不要写成字符串） | `params$name` 为数值 |
| `BaseSwitch` | 开关 | JSON 布尔，如 `true` | `isTRUE(params$name)` |
| `BaseSelect` | 下拉框 | 选中的单个字符串值（多选为字符串数组） | 选项文本即取值，`params$name` 为字符串 |
| `BaseColorPicker` | 颜色选择器 | 十六进制颜色字符串，如 `"#FF0000"` | 直接用于绘图颜色 |
| `FileSelect` | 上游结果文件 + 列选择器（见下节） | **对象**：`{"file": "<file id>", "<列名>": "<列名>"}` | `params$name$path` 为文件路径 |
| `Divider` | 分区标题（纯布局） | **不参与 request_param** | 不产生参数，不要读取 |

补充约定：
- `BaseSelect` 的候选项由数据源提供（`dataKey` / `resolver.accept_formats`）。固定取值（枚举）时，
  把候选值写在条目的 `data` 里（`[{ "label": "...", "value": "..." }]` 或字符串数组），
  `request_param` 里就填这些 `value` 之一；不要填展示用的 label。
- `BaseTextAreaNum` 的值是「逗号分隔的字符串」，不是数组；`request_param` 也必须给字符串。
- `BaseInputNumber` / `BaseSwitch` 请给原生 JSON 数字 / 布尔；不要给 `"0.05"` / `"true"` 这类字符串。
- `Divider` 只用于在表单上分组，不要给它 `required`，也不要把它当参数：它不会出现在 `request_param` 中，
  脚本里也不要读取它。
- 与文件 / 上游数据相关的输入（如 `FileSelect`，见下节）不按上表处理，它有自己的一套输入输出规则。

### FileSelect：从上游结果表里选文件 + 选列（下游分析的标准输入）

`FileSelect` 用于「当前脚本要读某个上游节点产出的表格文件，并指定读其中哪几列」。它是下游分析最常用的
输入控件，`request_param` 的形态与上面那些普通参数控件**完全不同**（它是对象，不是字符串 / 数字）。

#### 输入：io_schema 里怎么写

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
| `resolver.accept_formats` | 该输入接受的上游产物角色，通常 `["TABLE"]`；**上游 outputs 的 `role` 必须是 `TABLE`，这里才选得到** |
| `columns` | 需要在选中文件里挑选的列，每一项会渲染成一个「`<列名>` Columns」下拉框 |
| `modes[i]` | `1` 表示第 i 列可多选（多选值为数组），`0` 表示单选 |
| `columns_rules[i]` | `1` 表示第 i 列必填 |
| `groups[i]` | `1` 表示第 i 列额外渲染一个「`<列名>` Group Name」输入框（分组名） |

`groups` / `modes` / `columns_rules` 可以省略数组项，缺省按 `0` 处理。若只选文件、不需要指定列，
`columns` 也可以不写，此时表单只渲染「选文件」下拉框 + Open / List 按钮。

#### 输入：request_param 里怎么填

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

解析后（`resolved_inputs.<name>`）会得到：文件记录的字段（`id` / `analysis_result_id` / `file_name` /
`path` / `format` / `size` / `md5` 等）+ 你填的每一列 + `form_type: "FileSelect"` + `groups`（即 `columns` 列表）。
也就是说，脚本里拿到的 `params$exp` 是一个**对象**，文件路径在 `params$exp$path`，
列名在 `params$exp$sample_column` 之类的位置，而不是在 `params$exp` 这个字符串上。

#### 脚本里怎么用（qmd / R）

~~~r
# params$exp 是一个 list：文件信息 + 每一列选中的列名
exp_path    <- params$exp$path
sample_col  <- params$exp$sample_column
gene_col    <- params$exp$gene_column
value_col   <- params$exp$value_column

exp <- read.delim(exp_path, check.names = FALSE)
~~~

#### 输出：必须产出 role = TABLE 的表格，下游才能接着用

`FileSelect` 依赖上游 `outputs[].role == "TABLE"`，所以本脚本若产出的文件要给下游用，`io_schema.outputs`
必须显式写 `role`：

~~~json
{
  "outputs": [
    { "name": "tsv", "type": "file", "role": "TABLE" }
  ]
}
~~~

- 只写 `type: "file"` 时 `role` 默认是 `DEFAULT`，下游 `FileSelect` 的 `accept_formats: ["TABLE"]` 选不到；
- 产物需是可被解析列的表格（tsv / csv / xlsx），文件路径按 `outputs[].name` 写进 `outputs.json`；
- 若下游还要「按列取值」，保持首行为列名，列名不要加引号或特殊字符。

### request_param 生成示例

以上面的 inputs 为例，对应的 `request_param`（传给 build_analysis_node_params）为：

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
- 若 `request_param` 出现 `inputs` 里没有的键，会被忽略并返回 warning，必须回到本技能修正名字；
- 若必填项缺失，也会返回 warning，补齐后再运行。

## 在已创建的分析节点里写代码

基于 script_file_path 与 params.json 中的内容，根据用户要求完成代码撰写，使用最简单的代码，尽量不使用函数；
图片直接在 qmd 中打印，不要读取其他文件，只需要修改 script_file_path 文件中的内容。

- 读取 tsv / csv 时使用 tidyverse；无需确认查找 R 包（用户手动安装），只需保证代码简洁清楚即可。
- qmd 中打印表格需要使用 `knitr::kable()`。
- 当用户说「运行当前节点 / 跑一下这个节点」时，调用 run_analysis_node 工具，analysis_node_id 留空即可运行当前上下文节点。
- 用户给出其它节点 ID 时，把该 ID 原样传给 analysis_node_id。
- 工具会阻塞等待节点进入终态，返回 status / success / log；失败时先看 log 再判断是否需要修改脚本重跑。
- 修改代码后总是需要调用 run_analysis_node 重新运行分析。

如果 io_schema.json 中 `outputs` 有值，qmd 中需要添加输出，例如：

~~~json
"outputs": [
  {
    "name": "tsv",
    "type": "file",
    "role": "TABLE"
  }
]
~~~

~~~r
outputs <- list(
  tsv = mutate_genes_path
)
jsonlite::write_json(outputs, file.path(params$output_dir, "outputs.json"), auto_unbox = TRUE, pretty = TRUE)
~~~
