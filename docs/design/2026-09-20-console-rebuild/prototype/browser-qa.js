async (page) => {
  // Run via playwright-cli run-code; this verifies the standalone design artifact,
  // never the live product. Browser snapshots are generated into output/playwright.
  const base = "http://127.0.0.1:18422/prototype.html";
  const report = {
    pages: [],
    extraRoutes: [],
    interactions: [],
    responsive: [],
    errors: [],
    externalRequests: [],
  };
  page.on("pageerror", (error) => report.errors.push(error.message));
  page.on("request", (request) => {
    if (
      /^https?:/.test(request.url()) &&
      !request.url().startsWith("http://127.0.0.1:18422/")
    )
      report.externalRequests.push(request.url());
  });
  page.setDefaultTimeout(6000);
  const check = (ok, name, detail = "") => {
    report.interactions.push({ name, passed: Boolean(ok), detail });
    if (!ok) throw new Error(name + ": " + detail);
  };
  const settled = async () =>
    page.waitForFunction(
      () =>
        route === location.hash.slice(1).split("?")[0] &&
        query.toString() === new URLSearchParams(location.hash.slice(1).split('?')[1] || '').toString() &&
        document.querySelector("h1"),
    );
  const visit = async (path) => {
    await page.goto(base + "#" + path);
    await settled();
  };
  await page.goto(base);
  await page.reload();
  await visit("/home");
  await page.locator("#reset-demo").click();
  await page.evaluate(() => (document.getElementById("toast").hidden = true));
  await page.setViewportSize({ width: 1440, height: 1024 });
  const pages = await page.evaluate(() =>
    UX_PAGES.map((p) => ({ id: p[0], path: p[1], title: p[2] })),
  );
  const links = new Set();
  for (const item of pages) {
    await visit(item.path);
    const detail = await page.evaluate(() => ({
      title: document.querySelector("h1").textContent,
      overflow: document.documentElement.scrollWidth > innerWidth,
      links: [...document.querySelectorAll('a[href^="#/"]')].map((e) =>
        e.getAttribute("href").slice(1),
      ),
    }));
    if (detail.title.includes("没有找到") || detail.overflow)
      throw new Error("Route/layout failure: " + item.path);
    detail.links.forEach((link) => links.add(link));
    await page.screenshot({
      path: "output/playwright/console-rebuild/screens/" + item.id + ".png",
      fullPage: true,
      animations: "disabled",
    });
    report.pages.push({
      id: item.id,
      path: item.path,
      title: detail.title,
      passed: true,
    });
  }
  const additional = [...links].filter(
    (link) => !pages.some((p) => p.path === link),
  );
  const resourcePaths = await page.evaluate(() =>
    Object.keys(UX_RESOURCE_TYPES).map((key) => resourceBase(key)),
  );
  resourcePaths.forEach((path) =>
    additional.push(path + "/new", path + "/1?mode=edit"),
  );
  additional.push(
    "/tasks/new?step=2",
    "/tasks/new?step=3",
    "/tasks/24/output?type=GRPO",
    "/evaluations/new?datasetId=25",
    "/cleaning/new?datasetId=25",
  );
  for (const path of [...new Set(additional)]) {
    await visit(path);
    const title = await page.locator("h1").innerText();
    if (title.includes("没有找到"))
      throw new Error("Internal link is dead: " + path);
    report.extraRoutes.push({ path, passed: true });
  }
  await visit("/tasks/new");
  await page.locator("#task-topic").fill("");
  await page.getByRole("button", { name: "下一步 →" }).click();
  check(page.url().split("#")[1] === "/tasks/new", "必填主题阻止空提交");
  await page.locator("#task-topic").fill("仓储调度 QA");
  await page.getByRole("radio", { name: /GRPO/ }).check();
  await page.getByLabel("奖励档位", { exact: true }).fill("-1,0,0");
  await page.getByRole("button", { name: "下一步 →" }).click();
  check(!page.url().includes("step=2"), "奖励档位拒绝重复值");
  await page.getByLabel("奖励档位", { exact: true }).fill("-1,0,1");
  await page.getByRole("button", { name: "下一步 →" }).click();
  await page.waitForURL("**step=2");
  await settled();
  await page.getByLabel("领域数 n", { exact: true }).fill("2");
  await page.getByLabel("每领域方向数 m", { exact: true }).fill("3");
  await page.getByLabel("每方向问题数 x", { exact: true }).fill("4");
  check(
    (await page.locator("#scale-total").innerText()).includes("24"),
    "n/m/x 乘积随输入更新",
  );
  await page.getByRole("button", { name: "下一步 →" }).click();
  await page.waitForURL("**step=3");
  await settled();
  await page.getByRole("link", { name: "← 上一步" }).click();
  await settled();
  check(
    (await page.locator("#scale-n").inputValue()) === "2",
    "向导回退保留草稿",
  );
  await page.getByRole("button", { name: "下一步 →" }).click();
  await page.getByRole("button", { name: "创建并规划结构" }).click();
  await page.waitForURL("**/tasks/24");
  await settled();
  check(
    (await page.locator(".page-head").innerText()).includes("GRPO"),
    "创建后保留 GRPO 训练类型",
  );
  await page.getByRole("link", { name: "查看数据集", exact: true }).click();
  await settled();
  check(
    page.url().endsWith("/datasets/25"),
    "GRPO 创建到 GRPO 数据集没有回退 SFT",
  );
  await page.getByRole("link", { name: "浏览 GRPO 样本" }).click();
  await page.getByRole("link", { name: "#2024 · 多订单资源分配" }).click();
  await settled();
  check(
    (await page.locator("main").innerText()).includes("教师评判提示词"),
    "GRPO 单条详情有教师提示词及奖励判据",
  );
  await visit("/tasks/24/structure");
  await page.getByRole("button", { name: "确认结构并继续" }).click();
  check(!(await page.locator("dialog").isVisible()), "结构未保存不能确认");
  await page.getByRole("button", { name: "保存结构", exact: true }).click();
  await page.getByRole("button", { name: "确认结构并继续" }).click();
  await page.locator("#modal-confirm").click();
  await page.waitForURL("**/tasks/24/standards");
  await settled();
  check(true, "保存结构、确认后进入标准页");
  await visit("/datasets/24/samples");
  await page.getByLabel("筛选状态").selectOption("待复查");
  check(
    (await page.locator("[data-row]:visible").count()) === 1,
    "样本状态过滤",
  );
  await page.reload();
  check(
    (await page.getByLabel("筛选状态").inputValue()) === "待复查",
    "刷新恢复 URL 中筛选",
  );
  await page.getByLabel("选择样本 1024", { exact: true }).check();
  await page.getByRole("button", { name: "导出选中项" }).click();
  await settled();
  check(page.url().includes("selection=1024"), "批量导出携带所选样本 ID");
  await page.getByLabel("输出格式", { exact: true }).selectOption("CSV");
  await page.getByRole("button", { name: "创建导出文件" }).click();
  await page.waitForURL("**/exports/81");
  await settled();
  check(
    (await page.locator("main").innerText()).includes("CSV"),
    "导出详情保留所选格式",
  );
  const downloadPromise = page.waitForEvent("download");
  await page.getByRole("button", { name: "下载演示文件", exact: true }).click();
  const download = await downloadPromise;
  check(
    download.suggestedFilename() === "prototype-sft-demo.csv",
    "CSV 导出下载正确扩展名",
  );
  await download.saveAs(
    "output/playwright/console-rebuild/prototype-sft-demo.csv",
  );
  await visit("/datasets/25/exports/new");
  await visit('/datasets/24/exports');
  check((await page.locator('[data-row]').innerText()).includes('CSV'),'导出列表与详情格式一致');
  await visit('/datasets/24/samples');
  await page.getByLabel('选择样本 1026',{exact:true}).check();
  await page.getByRole('button',{name:'导出选中项'}).click();
  await settled();
  check((await page.locator('#export-code').innerText()).includes('月台和车辆'),'所选 ID 的预览匹配样本内容');
  await page.getByRole('button',{name:'创建导出文件'}).click();
  await settled();
  const selectedDownload = page.waitForEvent('download');
  await page.getByRole('button',{name:'下载演示文件',exact:true}).click();
  await (await selectedDownload).saveAs('output/playwright/console-rebuild/selected-1026.jsonl');
  await visit('/datasets/25/exports/new');
  await page.getByRole("button", { name: "创建 GRPO 导出" }).click();
  const grpoPromise = page.waitForEvent("download");
  await page.getByRole("button", { name: "下载 GRPO 演示文件" }).click();
  const grpo = await grpoPromise;
  await grpo.saveAs(
    "output/playwright/console-rebuild/prototype-grpo-demo.jsonl",
  );
  check(
    grpo.suggestedFilename() === "prototype-grpo-demo.jsonl",
    "GRPO 独立下载文件",
  );
  await visit("/evaluations/new");
  check(
    (await page.locator("input[type=checkbox]:disabled").count()) === 1,
    "生成者在裁判选择中禁用",
  );
  await page.getByLabel("抽样方式").selectOption("按比例");
  await page.locator("#sample-number").fill("50");
  check(
    (await page.locator("main aside").innerText()).includes("600 条"),
    "按比例抽样工作量同步更新",
  );
  await page.getByRole("button", { name: "创建并启动评估" }).click();
  await page.waitForURL("**/evaluations/36");
  await settled();
  check(
    (await page.locator("main").innerText()).includes("已入队"),
    "评估启动进入排队状态",
  );
  await page.getByRole("link", { name: "评估报告", exact: true }).click();
  await settled();
  check(
    !(await page.locator("main").innerText()).includes("样本整体表现较好"),
    "排队时不显示最终评估结论",
  );
  await page.waitForFunction(
    () => !JSON.parse(sessionStorage.getItem("ux.evalStarted")),
    { timeout: 5000 },
  );
  await visit("/evaluations/36/report");
  await page.reload();
  check(
    (await page.locator("h1").innerText()) === "评估报告",
    "报告刷新仍停在同一 run 报告",
  );
  await visit("/evaluations/new?datasetId=25");
  check(
    (await page.locator("main").innerText()).includes("GRPO") &&
      (await page.locator("main").innerText()).includes("尚未就绪"),
    "GRPO 评估适配未实现时阻止假启动",
  );
  await visit("/cleaning/new");
  await page.getByRole("button", { name: "预览 20 条样例" }).click();
  check(
    (await page.locator("#clean-preview").innerText()).includes("2 条疑似命中"),
    "清洗先预览命中",
  );
  await page.getByRole("button", { name: "开始清洗", exact: true }).click();
  await page.locator("#modal-confirm").click();
  await page.waitForURL("**/cleaning/18");
  await settled();
  check(true, "清洗提交进入独立报告");
  await visit("/cleaning/18/findings/7");
  await page.getByRole("radio", { name: "隔离：不用于当前交付" }).check();
  await page.locator("#review-note").fill("演示隔离判定");
  await page.getByRole("button", { name: "保存判断并返回" }).click();
  await page.waitForURL("**/findings");
  await settled();
  check(
    (await page.locator("[data-row]").first().innerText()).includes("已隔离"),
    "隔离判断回写命中列表",
  );
  await visit("/cleaning/18/findings/7");
  await page.getByRole("radio", { name: "保留：有效样本" }).check();
  await page.locator("#review-note").fill("关键词来自引用，内容有效");
  await page.getByRole("button", { name: "保存判断并返回" }).click();
  await page.waitForURL("**/findings");
  await settled();
  check(
    (await page.locator("[data-row]").first().innerText()).includes("已保留"),
    "保留判断回写命中列表",
  );
  await visit("/tasks/24/runs/104");
  await page.getByRole("button", { name: "仅重试失败部分" }).click();
  await page.locator("#modal-confirm").click();
  check(
    await page.getByRole("button", { name: "已请求续跑" }).isDisabled(),
    "续跑状态保留且阻止重复点击",
  );
  await visit("/resources/keywords/1");
  await page.getByRole("button", { name: "停用", exact: true }).click();
  await page.locator("#modal-confirm").click();
  await page.getByRole("link", { name: "返回关键词库" }).click();
  await settled();
  check(
    (await page.locator("[data-row]").innerText()).includes("已停用"),
    "资源停用状态同步列表",
  );
  await visit("/resources/keywords/1");
  await page.getByRole("link", { name: "编辑", exact: true }).click();
  await page.getByLabel("匹配文本", { exact: true }).fill("无法完成");
  await page.getByRole("button", { name: "保存关键词库" }).click();
  await page.waitForURL("**/keywords/1");
  await settled();
  check(
    (await page.locator("main").innerText()).includes("无法完成"),
    "资源字段修改同步详情",
  );
  await page.getByRole("button", { name: "删除关键词" }).click();
  await page.locator("#modal-confirm").click();
  await page.waitForURL("**/keywords");
  await settled();
  check(
    (await page.locator("[data-row]").count()) === 0,
    "删除关键词后列表移除记录",
  );
  await visit("/notifications");
  await visit('/resources/dimensions/1');
  await page.getByRole('link',{name:'复制为自定义维度'}).click();
  await settled();
  await page.getByLabel('名称',{exact:true}).fill('逻辑一致性 · 实验副本');
  await page.getByRole('button',{name:'保存评估维度'}).click();
  await page.waitForURL('**/dimensions/2');
  await settled();
  check((await page.locator('main').innerText()).includes('实验副本'),'复制维度保存为新对象');
  await visit('/resources/dimensions/1');
  check(await page.getByRole('link',{name:'复制为自定义维度'}).count()===1 && !(await page.locator('main').innerText()).includes('实验副本'),'复制不改变内置维度及只读状态');
  await visit('/resources/dimensions');
  check(await page.locator('[data-row]').count()===2,'维度列表同时保留内置与副本');
  await visit('/notifications');
  await page.getByRole("button", { name: "全部标为已读" }).click();
  check(
    (await page.locator("#main .badge").filter({ hasText: "已读" }).count()) ===
      3,
    "已读状态回写通知",
  );
  await visit("/admin/providers/1");
  await page.locator("#role").selectOption("member");
  check(
    (await page.locator("h1").innerText()).includes("没有访问"),
    "普通成员不能访问模型配置",
  );
  await visit("/resources/prompts/1");
  check(
    (await page.locator("h1").innerText()).includes("没有访问"),
    "普通成员不能绕资源库访问 admin 配置",
  );
  await page.locator("#role").selectOption("admin");
  await page.locator('#role').selectOption('member');
  await visit('/datasets/24/exports/new');
  await page.getByRole('button',{name:'查看映射规则 →'}).click();
  check((await page.locator('dialog').innerText()).includes('只读映射快照'),'普通成员可查看导出映射快照');
  await page.locator('#modal-confirm').click();
  await visit('/resources/dimensions');
  check(await page.locator('.subnav a[href="#/resources/mappings"]').count()===0,'成员子导航不显示无权限的管理入口');
  await page.locator('#role').selectOption('admin');
  await visit("/home");
  for (const state of [
    "empty",
    "loading",
    "error",
    "offline",
    "denied",
    "missing",
  ]) {
    await page.locator("#scenario").selectOption(state);
    check((await page.locator("h1").count()) === 1, "异常场景 " + state);
    await page.screenshot({
      path: "output/playwright/console-rebuild/screens/state-" + state + ".png",
      fullPage: true,
    });
  }
  await page.locator("#scenario").selectOption("normal");
  await page.locator("#reset-demo").click();
  await page.evaluate(() => document.getElementById('toast').hidden = true);
  for (const width of [1024, 390]) {
    await page.setViewportSize({ width, height: 900 });
    for (const path of [
      "/home",
      "/datasets/24/samples",
      "/tasks/new",
      "/evaluations/36/report",
      "/admin/providers/new",
    ]) {
      await visit(path);
      const overflow = await page.evaluate(
        () => document.documentElement.scrollWidth > innerWidth,
      );
      report.responsive.push({ width, path, passed: !overflow });
      if (overflow)
        throw new Error("Responsive overflow " + width + " " + path);
    }
    await page.screenshot({
      path:
        "output/playwright/console-rebuild/screens/responsive-" +
        width +
        ".png",
      fullPage: true,
    });
  }
  await page.setViewportSize({ width: 1440, height: 1024 });
  await visit("/home");
  check(
    report.errors.length === 0,
    "无未处理 JavaScript 异常",
    report.errors.join(";"),
  );
  check(report.externalRequests.length === 0, "原型不请求业务接口或外部服务");
  return report;
}
