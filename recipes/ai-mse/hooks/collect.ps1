# ============================================================================
# ai-mse recipe 采集钩子（ps1 版；collect.sh 为 Linux/Git Bash 等价实现）：
#   构建源码制品（聚合 fat-jar / 逐服务镜像 / UI zip）→ 写 <work>\build-manifest.json 交核心。
#   只管「构建与产物声明」——登录/推送/打包/轮询全部在 collect-kit 核心。
#
# 参数（由 collect-kit 核心传入）：-Repo -Work -Name [-Version] [-Form mono|micro]
#   [-Ui true|false] [-BaseVersion] [-Out] [-Modules a,b] [-Middleware x,y] [-Repush]
# 环境变量：COLLECT_KIT_BIN=collect-kit 可执行文件路径（zip-dir 等通用工具）。
# ============================================================================
param(
    [Parameter(Mandatory = $true)][string]$Repo,
    [Parameter(Mandatory = $true)][string]$Work,
    [Parameter(Mandatory = $true)][string]$Name,
    [string]$Version = "1.0.0",
    [string]$Form = "mono",
    [string]$Ui = "true",
    [string]$BaseVersion = "",
    [string]$Out = "",
    [string]$Modules = "",
    [string]$Middleware = "",
    [switch]$Repush
)
$ErrorActionPreference = "Stop"
# stdout 重定向（Git Bash/管道/向导子进程）时 PS5.1 缺省按 OEM 代码页写中文 → UTF-8 终端乱码；统一 UTF-8
[Console]::OutputEncoding = [System.Text.Encoding]::UTF8
if (-not (Test-Path $Repo)) { throw "collect.ps1: 仓库根不存在: $Repo" }

function Assert-LastExit([string]$step) {
    if ($LASTEXITCODE -ne 0) { throw "$step 失败（exit=$LASTEXITCODE）" }
}

function Write-Utf8NoBom([string]$path, [string]$content) {
    # PS 5.1 Set-Content -Encoding UTF8 带 BOM：pom.xml/nginx.conf 等被严格解析器读取时 BOM 致解析失败
    $enc = New-Object System.Text.UTF8Encoding($false)
    [System.IO.File]::WriteAllText($path, $content, $enc)
}

if (-not $BaseVersion) {
    $frameworkPom = Join-Path $Repo "mse-framework\pom.xml"
    if (Test-Path $frameworkPom) {
        if ($frameworkPom -and (Select-String -Path $frameworkPom -Pattern "<version>([^<]+)</version>" | Select-Object -First 1) -match "<version>([^<]+)</version>") {
            $BaseVersion = $Matches[1].Trim()
        }
    }
    if (-not $BaseVersion) { $BaseVersion = "1.0.0-SNAPSHOT" }
}
$zipTool = $env:COLLECT_KIT_BIN
if (-not $zipTool) { throw "collect.ps1: 缺少 COLLECT_KIT_BIN 环境变量（须由 collect-kit 核心调起）" }
$withUi = ($Ui -ne "false")
$build = Join-Path $Work "build"
$moduleList = @($Modules -split "," | ForEach-Object { $_.Trim() } | Where-Object { $_ })
$mwList = @($Middleware -split "," | ForEach-Object { $_.Trim() } | Where-Object { $_ })
$uiZip = Join-Path $Work "mse-ui-$Version.zip"

$script:appImages = [System.Collections.Generic.List[string]]::new()
$script:materials = [System.Collections.Generic.List[object]]::new()
$script:summary = [System.Collections.Generic.List[string]]::new()
function Add-Img([string]$ref) { $script:appImages.Add($ref) }
function Add-Mat([string]$path, [string]$appCode, [string]$role = "") {
    $m = @{ path = $path; kind = "artifact"; appCode = $appCode; remark = "collect-kit $Name-$Version" }
    if ($role) { $m.role = $role }
    $script:materials.Add($m)
}
function Add-Sum([string]$line) { $script:summary.Add($line) }

if ($Repush) {
    # ---- 重推：跳过构建，校验既有制品后重写 manifest（仅 mono——核心按 recipe 声明拦截）----
    $jar = Join-Path $build "target\mse-tenant-standalone.jar"
    if (-not (Test-Path $jar)) { throw "collect.ps1: 重推缺少既有 fat-jar: $jar（先跑一次构建，或核对 -Name/-Version/-Out）" }
    if ($withUi -and -not (Test-Path $uiZip)) { throw "collect.ps1: 重推缺少既有 UI zip: $uiZip（构建时带了 -Ui？核对勾选与目录）" }
    Add-Img "mse/mse-monolith:$Version"
    Add-Mat "build/target/mse-tenant-standalone.jar" "mse-monolith"
    if ($withUi) { Add-Mat "mse-ui-$Version.zip" "mse-monolith" "ui" }
    Add-Sum "重推既有制品（复用 $Work）"
} else {
    if (-not (Get-Command docker -ErrorAction SilentlyContinue)) { throw "collect.ps1: 需要 docker（应用镜像构建）" }
    if ($Form -eq "micro") {
        Write-Host "==> [hook] 微服务形态：基座五件套 + 勾选业务 boot → 逐服务镜像（配置走 Nacos 基线）"
        Write-Host "    前提：本地仓库已 install 底座与所选业务工程（全仓 sh scripts/build-all.sh）"
        function Build-MicroBoot([string]$code, [string]$port, [string]$dir) {
            if (-not (Test-Path $dir)) { throw "collect.ps1: 单元 $code 的 boot 目录不存在: $dir" }
            Push-Location $dir
            try {
                mvn -q package -DskipTests
                Assert-LastExit "mvn package（$code）"
            } finally { Pop-Location }
            docker build -f (Join-Path $Repo "mse-framework\mse-boot\Dockerfile.prebuilt") `
                --build-arg PORT=$port -t "mse/mse-${code}:$Version" $dir
            if ($LASTEXITCODE -ne 0) { throw "collect.ps1: 服务镜像 mse/mse-$code 构建失败（在线拉基础镜像受限？）" }
            Add-Img "mse/mse-${code}:$Version"
            Add-Sum "服务镜像 mse/mse-${code}:$Version（:$port）"
        }
        Build-MicroBoot gateway 48080 (Join-Path $Repo "mse-framework\mse-boot\boot-gateway")
        Build-MicroBoot system 48081 (Join-Path $Repo "mse-framework\mse-boot\boot-system")
        Build-MicroBoot platform 48082 (Join-Path $Repo "mse-framework\mse-boot\boot-platform")
        Build-MicroBoot auth 48083 (Join-Path $Repo "mse-framework\mse-boot\boot-auth")
        Build-MicroBoot job 48084 (Join-Path $Repo "mse-framework\mse-boot\boot-job")
        # 业务单元端口表（与各工程 docker/nacos/config/<svc>.yaml 一致）
        $bizPort = @{ oa = 8090; mp = 8091; open = 8092; data = 8093; ai = 8094; fs = 8095; search = 8096; monitor = 8097; mes = 8098; im = 8099 }
        foreach ($m in $moduleList) {
            if (-not $bizPort.ContainsKey($m)) { throw "collect.ps1: 未知业务模块 $m（端口表无登记）" }
            Build-MicroBoot $m $bizPort[$m] (Join-Path $Repo "mse-modules\mse-$m\$m-boot")
        }
    } else {
        Write-Host "==> [hook] 组装租户单体聚合工程（tenant-starter + $($moduleList -join ',')）→ fat-jar → 应用镜像"
        if (-not (Get-Command mvn -ErrorAction SilentlyContinue)) { throw "collect.ps1: 需要 mvn" }
        $appDir = Join-Path $build "src\main\java\vip\mseapp\boot\tenantstandalone"
        New-Item -ItemType Directory -Force -Path $appDir | Out-Null
        $depLines = ($moduleList | ForEach-Object {
            "        <dependency><groupId>vip.mseapp</groupId><artifactId>$($_)-service</artifactId><version>$BaseVersion</version></dependency>`r`n"
        }) -join ""
        $mqDep = ""
        if ($mwList -contains "rocketmq") {
            $mqDep = "        <dependency><groupId>vip.mseapp</groupId><artifactId>mq-starter</artifactId><version>$BaseVersion</version></dependency>`r`n"
        }
        $pom = @"
<?xml version="1.0" encoding="UTF-8"?>
<project xmlns="http://maven.apache.org/POM/4.0.0"
         xmlns:xsi="http://www.w3.org/2001/XMLSchema-instance"
         xsi:schemaLocation="http://maven.apache.org/POM/4.0.0 https://maven.apache.org/xsd/maven-4.0.0.xsd">
    <modelVersion>4.0.0</modelVersion>
    <parent>
        <groupId>vip.mseapp</groupId>
        <artifactId>mse-starter-parent</artifactId>
        <version>$BaseVersion</version>
        <relativePath/>
    </parent>
    <artifactId>mse-tenant-standalone</artifactId>
    <dependencies>
        <!-- 私有部署形态入口：service-system/service-auth + starter-bridge + standalone 装配（含导入端点）。
             框架内部模块不经 BOM 仲裁，须显式版本（与 mse-starter-parent 同源升级） -->
        <dependency><groupId>vip.mseapp</groupId><artifactId>tenant-starter</artifactId><version>$BaseVersion</version></dependency>
$mqDep$depLines    </dependencies>
    <build>
        <finalName>mse-tenant-standalone</finalName>
        <plugins>
            <plugin>
                <groupId>org.springframework.boot</groupId>
                <artifactId>spring-boot-maven-plugin</artifactId>
                <!-- 父 pom 仅 pluginManagement 管版本：fat-jar 需显式绑定 repackage（同 mes-boot 写法） -->
                <executions>
                    <execution>
                        <goals>
                            <goal>repackage</goal>
                        </goals>
                    </execution>
                </executions>
            </plugin>
        </plugins>
    </build>
</project>
"@
        Write-Utf8NoBom (Join-Path $build "pom.xml") $pom

        $main = @'
package vip.mseapp.boot.tenantstandalone;

import org.springframework.boot.SpringApplication;
import org.springframework.boot.autoconfigure.SpringBootApplication;

/**
 * 租户私有部署单体启动器（standalone 形态）：纯 @SpringBootApplication，
 * 全部能力经各积木 SPI 自动装配（含 tenant-starter 的导入端点与启动自足化）。
 */
@SpringBootApplication
public class TenantStandaloneApplication {

    public static void main(String[] args) {
        SpringApplication.run(TenantStandaloneApplication.class, args);
    }
}
'@
        Write-Utf8NoBom (Join-Path $appDir "TenantStandaloneApplication.java") $main

        Push-Location $build
        try {
            mvn -q package -DskipTests
            Assert-LastExit "mvn package"
        } finally { Pop-Location }
        Add-Mat "build/target/mse-tenant-standalone.jar" "mse-monolith"
        Add-Sum "租户单体 fat-jar（build\target\mse-tenant-standalone.jar）"

        # 应用镜像在本机 docker 构建（唯一交付形态）；镜像导出由 deliver-studio 打包时完成
        docker build -f (Join-Path $Repo "mse-framework\mse-boot\Dockerfile.prebuilt") `
            --build-arg PORT=8080 -t "mse/mse-monolith:$Version" $build
        if ($LASTEXITCODE -ne 0) { throw "collect.ps1: 应用镜像构建失败（在线拉基础镜像受限？）——镜像为唯一交付形态，中止" }
        Add-Img "mse/mse-monolith:$Version"
        Add-Sum "应用镜像 mse/mse-monolith:$Version（本机 docker；工作台打包时导出）"
    }

    if ($withUi) {
        Write-Host "==> [hook] 前端：租户端 dist + 移动基站 + 已选模块子应用 → UI zip"
        if (-not (Get-Command npm -ErrorAction SilentlyContinue)) { throw "collect.ps1: 需要 npm（前端构建）" }
        $baseUi = Join-Path $Repo "mse-framework\mse-ui"
        if (-not (Test-Path (Join-Path $baseUi "package.json"))) { throw "collect.ps1: 未找到 $baseUi" }
        # 租户离线交付只含租户端（build:tenant → dist\tenant 放 nginx 根路径）；平台端不进包
        Push-Location $baseUi
        try {
            npm install --no-audit --no-fund
            Assert-LastExit "npm install（mse-ui）"
            npm run build:tenant
            Assert-LastExit "npm run build:tenant（mse-ui）"
        } finally { Pop-Location }
        $uiHtml = Join-Path $build "ui\html"
        if (Test-Path $uiHtml) { Remove-Item $uiHtml -Recurse -Force }
        New-Item -ItemType Directory -Path $uiHtml -Force | Out-Null
        Copy-Item (Join-Path $baseUi "dist\tenant\*") "$uiHtml\" -Recurse -Force
        $baseMobileUi = Join-Path $Repo "mse-framework\mse-ui-mobile"
        if (Test-Path (Join-Path $baseMobileUi "package.json")) {
            Push-Location $baseMobileUi
            try {
                npm install --no-audit --no-fund
                Assert-LastExit "npm install（mse-ui-mobile）"
                npm run build
                Assert-LastExit "npm run build（mse-ui-mobile）"
            } finally { Pop-Location }
            New-Item -ItemType Directory -Force -Path "$uiHtml\m" | Out-Null
            Copy-Item (Join-Path $baseMobileUi "dist\*") "$uiHtml\m\" -Recurse -Force
            Write-Host "    m/（移动基站前端已收集）"
        }
        $uiSubs = [System.Collections.Generic.List[string]]::new()
        $uiMobiles = [System.Collections.Generic.List[string]]::new()
        foreach ($m in $moduleList) {
            $uiDir = Join-Path $Repo "mse-modules\mse-$m\$m-ui"
            if (Test-Path (Join-Path $uiDir "package.json")) {
                Push-Location $uiDir
                try {
                    npm install --no-audit --no-fund
                    Assert-LastExit "npm install（$m-ui）"
                    npm run build
                    Assert-LastExit "npm run build（$m-ui）"
                } finally { Pop-Location }
                New-Item -ItemType Directory -Force -Path "$uiHtml\sub-apps\$m" | Out-Null
                Copy-Item (Join-Path $uiDir "dist\*") "$uiHtml\sub-apps\$m\" -Recurse -Force
                Write-Host "    sub-apps/$m（前端已收集）"
                $uiSubs.Add($m)
            } else {
                Write-Host "    跳过模块 $m（未找到 $uiDir）"
                Add-Sum "模块 $m 前端：未找到 $m-ui，PC 子应用未收集"
            }
            $mUiDir = Join-Path $Repo "mse-modules\mse-$m\$m-mobile-ui"
            if (Test-Path (Join-Path $mUiDir "package.json")) {
                Push-Location $mUiDir
                try {
                    npm install --no-audit --no-fund
                    Assert-LastExit "npm install（$m-mobile-ui）"
                    npm run build
                    Assert-LastExit "npm run build（$m-mobile-ui）"
                } finally { Pop-Location }
                New-Item -ItemType Directory -Force -Path "$uiHtml\m-apps\$m" | Out-Null
                Copy-Item (Join-Path $mUiDir "dist\*") "$uiHtml\m-apps\$m\" -Recurse -Force
                Write-Host "    m-apps/$m（移动子应用前端已收集）"
                $uiMobiles.Add($m)
            }
        }
        $uiLine = "前端物料：基座 dist + 移动基站 m/"
        if ($uiSubs.Count -gt 0) { $uiLine += " + PC 子应用 $($uiSubs -join ' ')" }
        if ($uiMobiles.Count -gt 0) { $uiLine += " + 移动子应用 $($uiMobiles -join ' ')" }
        Add-Sum "$uiLine（UI zip，web 单元应用包）"

        # nginx.conf 放 UI zip 根（web 单元 Dockerfile 模板引用根下 nginx.conf）
        $nginx = @'
server {
    listen 80;
    root /usr/share/nginx/html;
    index index.html;
    location / { try_files $uri $uri/ /index.html; }
    location /api/ { proxy_pass http://app:8080/; proxy_set_header X-Forwarded-Host $host; proxy_read_timeout 300s; proxy_send_timeout 300s; }
    # 各勾选模块子应用经构建收集至 html/sub-apps/<m>/，由此统一挂载（base=/sub-apps/<m>/）
    location /sub-apps/ { alias /usr/share/nginx/html/sub-apps/; try_files $uri $uri/ /index.html; }
    # 移动基站（mse-ui-mobile，base=/m/）与移动子应用（base=/m-apps/<m>/）
    location /m/ { alias /usr/share/nginx/html/m/; try_files $uri $uri/ /m/index.html; }
    location /m-apps/ { alias /usr/share/nginx/html/m-apps/; try_files $uri $uri/ /m-apps/index.html; }
}
'@
        if ($Form -eq "micro") {
            # 微服务形态：/api 反代到网关（gateway:48080）而非单体 app
            $nginx = $nginx.Replace("proxy_pass http://app:8080/;", "proxy_pass http://gateway:48080/;")
        }
        Write-Utf8NoBom (Join-Path $uiHtml "nginx.conf") $nginx
        if (Test-Path $uiZip) { Remove-Item $uiZip -Force }
        & $zipTool zip-dir $uiHtml $uiZip
        if ($LASTEXITCODE -ne 0) { throw "collect.ps1: UI zip 打包失败" }
        Add-Mat "mse-ui-$Version.zip" "mse-monolith" "ui"
        Add-Sum "UI zip（mse-ui-$Version.zip，web 单元应用包）"
    } else {
        Write-Host "    跳过前端（-Ui false，打包用无前端变体应用 mse-monolith-headless）"
    }
}

# ---- 打包请求构造（componentCodes = 勾选中间件 + micro 形态 nacos——勾什么打包什么；未勾 mysql/redis 时套件不含对应组件，DB/Redis 端点经应用安装变量接外部库）----
$components = [System.Collections.Generic.List[string]]::new()
if ($Form -eq "micro") { $components.Add("nacos") }   # 形态基础设施（非中间件选项），恒入包
foreach ($mw in $mwList) { if (-not $components.Contains($mw)) { $components.Add($mw) } }

$units = [System.Collections.Generic.List[object]]::new()
if ($Form -eq "micro") {
    $appCode = "mse-micro"
    foreach ($code in @("gateway", "system", "platform", "auth", "job")) {
        $units.Add(@{ code = $code; source = "image_ref"; imageRef = "mse/mse-${code}:$Version" })
    }
    $cc = [System.Collections.Generic.List[string]]::new()
    foreach ($id in @("mse-micro-common.yaml", "mse-micro-db.yaml", "mse-micro-db-biz.yaml", "mse-micro-dubbo.yaml",
            "mse-gateway.yaml", "mse-system.yaml", "mse-platform.yaml", "mse-auth.yaml", "mse-job.yaml")) {
        $cc.Add($id)
    }
    foreach ($m in $moduleList) {
        $units.Add(@{ code = $m; source = "image_ref"; imageRef = "mse/mse-${m}:$Version" })
        $cc.Add("mse-$m.yaml")
    }
    if ($withUi) { $units.Add(@{ code = "web"; source = "artifact"; materialRole = "ui" }) }
} else {
    $appCode = if ($withUi) { "mse-monolith" } else { "mse-monolith-headless" }
    $units.Add(@{ code = "app"; source = "image_ref"; imageRef = "mse/mse-monolith:$Version" })
    if ($withUi) { $units.Add(@{ code = "web"; source = "artifact"; materialRole = "ui" }) }
}

$package = @{ appCode = $appCode; units = $units; componentCodes = $components; buildVars = @{} }
if ($Form -eq "micro") { $package.configCenterInclude = $cc }

$manifest = @{ appImages = $appImages; materials = $materials; package = $package; summary = $summary }
$manifestPath = Join-Path $Work "build-manifest.json"
Write-Utf8NoBom $manifestPath ($manifest | ConvertTo-Json -Depth 6)
Write-Host "==> [hook] 产物合同已写出: $manifestPath"
