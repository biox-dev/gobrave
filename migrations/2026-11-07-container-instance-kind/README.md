# container instance kind

`go_container_instance` 新增 `kind` 列，记录容器的「运行形态」：

- `web`：Web 应用容器（跑模板命令、暴露服务），owner 为 `app_session`；
- `terminal`：终端会话容器（keep-alive 常驻，真正的 shell 由 exec 打开），owner 同样是 `app_session`；
- `dag_node`：DAG 节点任务容器（`bash ./run.sh`，跑完即退出）。

它和 `owner_type` 是两个维度：`owner_type` 说明容器属于谁（用于生命周期与回收），
`kind` 说明容器怎么跑（`ContainerCreateWorker.executeCreateInstance` 按它组装运行时
spec 的启动命令 / workload 类型 / 运行用户）。

历史数据回填：`owner_type = 'dag_node'` 的实例为 `dag_node`，其余（包括终端会话容器，
它们与 Web 应用容器共用 `app_session`）保持默认 `web`。终端会话容器由创建时显式写入，
回填无法区分，属于可接受的降级：旧记录只有在被重新创建时才会带上正确形态。
