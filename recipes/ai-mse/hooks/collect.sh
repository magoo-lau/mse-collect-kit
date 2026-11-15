#!/usr/bin/env bash
# ============================================================================
# ai-mse recipe 采集钩子（sh 版；collect.ps1 为 Windows 等价实现）：
#   构建源码制品（聚合 fat-jar / 逐服务镜像 / UI zip）→ 写 <work>/build-manifest.json 交核心。
#   只管「构建与产物声明」——登录/推送/打包/轮询全部在 collect-kit 核心（仓库知识与 studio
#   知识被产物合同隔离在钩子与核心两侧）。
#
# 参数（由 collect-kit 核心传入）：
#   --repo <仓库根> --work <工作目录> --name <套件名> --version <版本>
#   [--form mono|micro] [--ui true|false] [--base-version <基座版本>] [--out <目录>]
#   [--modules oa,fs] [--middleware rocketmq,...] [--repush]
#
# 产物合同（build-manifest.json，路径相对 work）：
#   appImages[] 需入仓的应用镜像；materials[] 上传制品（role=ui 的 materialId 供打包单元回填）；
#   package 打包请求构造（materialRole 由核心换成实际 materialId）；summary[] 结果汇总行。
# 环境变量：COLLECT_KIT_BIN=collect-kit 可执行文件路径（zip-dir 等通用工具）。
# ============================================================================
set -euo pipefail

REPO="" WORK="" NAME="" VERSION="1.0.0" FORM="mono" UI="true" BASE_VERSION="" OUT="" MODULES="" MIDDLEWARE="" REPUSH=false
while [[ $# -gt 0 ]]; do
  case "$1" in
    --repo) REPO="$2"; shift 2 ;;
    --work) WORK="$2"; shift 2 ;;
    --name) NAME="$2"; shift 2 ;;
    --version) VERSION="$2"; shift 2 ;;
    --form) FORM="$2"; shift 2 ;;
    --ui) UI="$2"; shift 2 ;;
    --base-version) BASE_VERSION="$2"; shift 2 ;;
    --out) OUT="$2"; shift 2 ;;
    --modules) MODULES="$2"; shift 2 ;;
    --middleware) MIDDLEWARE="$2"; shift 2 ;;
    --repush) REPUSH=true; shift ;;
    *) echo "collect.sh: 未知参数 $1"; exit 1 ;;
  esac
done
[[ -n "$REPO" && -n "$WORK" && -n "$NAME" ]] || { echo "collect.sh: 缺少 --repo/--work/--name"; exit 1; }
[[ -d "$REPO" ]] || { echo "collect.sh: 仓库根不存在: $REPO"; exit 1; }
if [[ -z "$BASE_VERSION" ]]; then
  BASE_VERSION="$(grep -m1 -o '<version>[^<]*</version>' "$REPO/mse-framework/pom.xml" | sed 's/<[^>]*>//g' || true)"
  [[ -z "$BASE_VERSION" ]] && BASE_VERSION="1.0.0-SNAPSHOT"
fi
BUILD="$WORK/build"
ZIP_TOOL="${COLLECT_KIT_BIN:-}"
[[ -z "$ZIP_TOOL" ]] && { echo "collect.sh: 缺少 COLLECT_KIT_BIN 环境变量（须由 collect-kit 核心调起）"; exit 1; }

MODULE_ARRAY=()
IFS=',' read -r -a MODULE_ARRAY <<< "$MODULES"
MW_ARRAY=()
IFS=',' read -r -a MW_ARRAY <<< "$MIDDLEWARE"
has_mw() { for m in "${MW_ARRAY[@]}"; do [[ "$m" == "$1" ]] && return 0; done; return 1; }
mod_list() { # 规范化模块清单（空格剔除）
  local out="" m
  for m in "${MODULE_ARRAY[@]}"; do m="$(echo "$m" | xargs)"; [[ -z "$m" ]] && continue; out="$out,$m"; done
  echo "${out#,}"
}

# ---- manifest 素材累积 ----
APP_IMAGES_JSON=""    # "ref","ref"
MATERIALS_JSON=""     # {...},{...}
SUMMARY_JSON=""       # "...","..."
json_str() { printf '"%s"' "$(echo "$1" | sed 's/\\/\\\\/g; s/"/\\"/g')"; }
add_image() { [[ -n "$APP_IMAGES_JSON" ]] && APP_IMAGES_JSON="$APP_IMAGES_JSON,"; APP_IMAGES_JSON="$APP_IMAGES_JSON$(json_str "$1")"; }
add_material() { # path kind appCode role
  [[ -n "$MATERIALS_JSON" ]] && MATERIALS_JSON="$MATERIALS_JSON,"
  local role="$4"; [[ -n "$role" ]] && role="\"role\":$(json_str "$role"),"
  MATERIALS_JSON="$MATERIALS_JSON{\"path\":$(json_str "$1"),\"kind\":$(json_str "$2"),\"appCode\":$(json_str "$3"),${role}\"remark\":\"collect-kit $NAME-$VERSION\"}"
}
add_summary() { [[ -n "$SUMMARY_JSON" ]] && SUMMARY_JSON="$SUMMARY_JSON,"; SUMMARY_JSON="$SUMMARY_JSON$(json_str "$1")"; }

UI_ZIP=""
if [[ "$UI" == "true" ]]; then
  UI_ZIP="$WORK/mse-ui-$VERSION.zip"
fi

if [[ "$REPUSH" == "true" ]]; then
  # ---- 重推：跳过构建，校验既有制品后重写 manifest（仅 mono 形态——核心已按 recipe 声明拦截）----
  JAR="$BUILD/target/mse-tenant-standalone.jar"
  [[ -f "$JAR" ]] || { echo "collect.sh: 重推缺少既有 fat-jar: $JAR（先跑一次构建，或核对 --name/--version/--out）"; exit 1; }
  if [[ "$UI" == "true" ]]; then
    [[ -f "$UI_ZIP" ]] || { echo "collect.sh: 重推缺少既有 UI zip: $UI_ZIP（构建时带了 --ui？核对勾选与目录）"; exit 1; }
  fi
  add_image "mse/mse-monolith:$VERSION"
  add_material "build/target/mse-tenant-standalone.jar" artifact mse-monolith ""
  [[ "$UI" == "true" ]] && add_material "mse-ui-$VERSION.zip" artifact mse-monolith ui
  add_summary "重推既有制品（复用 $WORK）"
else
  command -v docker >/dev/null || { echo "collect.sh: 需要 docker（应用镜像构建）"; exit 1; }
  if [[ "$FORM" == "micro" ]]; then
    # ---- 微服务形态：基座五件套 + 勾选业务 boot 逐服务 mvn package → 镜像 ----
    echo "==> [hook] 微服务形态：基座五件套 + 勾选业务 boot → 逐服务镜像（配置走 Nacos 基线）"
    echo "    前提：本地仓库已 install 底座与所选业务工程（全仓 sh scripts/build-all.sh）"
    build_boot() { # $1=编码 $2=端口 $3=boot 目录
      local code="$1" port="$2" dir="$3" img="mse/mse-$code"
      [[ -d "$dir" ]] || { echo "collect.sh: 单元 $code 的 boot 目录不存在: $dir"; exit 1; }
      (cd "$dir" && mvn -q package -DskipTests) || { echo "collect.sh: $code mvn package 失败（依赖未 install？）"; exit 1; }
      docker build -f "$REPO/mse-framework/mse-boot/Dockerfile.prebuilt" \
          --build-arg PORT="$port" -t "$img:$VERSION" "$dir" \
        || { echo "collect.sh: 服务镜像 $img 构建失败（在线拉基础镜像受限？）"; exit 1; }
      add_image "$img:$VERSION"
      add_summary "服务镜像 $img:$VERSION（:$port）"
    }
    build_boot gateway  48080 "$REPO/mse-framework/mse-boot/boot-gateway"
    build_boot system   48081 "$REPO/mse-framework/mse-boot/boot-system"
    build_boot platform 48082 "$REPO/mse-framework/mse-boot/boot-platform"
    build_boot auth     48083 "$REPO/mse-framework/mse-boot/boot-auth"
    build_boot job      48084 "$REPO/mse-framework/mse-boot/boot-job"
    # 业务单元端口表（与各工程 docker/nacos/config/<svc>.yaml 一致）
    declare -A BIZ_PORT=([oa]=8090 [mp]=8091 [open]=8092 [data]=8093 [ai]=8094 [fs]=8095 [search]=8096 [monitor]=8097 [mes]=8098 [im]=8099)
    for m in "${MODULE_ARRAY[@]}"; do
      m="$(echo "$m" | xargs)"; [[ -z "$m" ]] && continue
      [[ -n "${BIZ_PORT[$m]:-}" ]] || { echo "collect.sh: 未知业务模块 $m（端口表无登记）"; exit 1; }
      build_boot "$m" "${BIZ_PORT[$m]}" "$REPO/mse-modules/mse-$m/$m-boot"
    done
  else
    # ---- 单体形态：聚合工程（tenant-starter + 勾选积木）→ fat-jar → 应用镜像 ----
    echo "==> [hook] 组装租户单体聚合工程（tenant-starter + $(mod_list)）→ fat-jar → 应用镜像"
    command -v mvn >/dev/null || { echo "collect.sh: 需要 mvn"; exit 1; }
    mkdir -p "$BUILD/src/main/java/vip/mseapp/boot/tenantstandalone"
    NL=$'\n'
    DEP_LINES=""
    for m in "${MODULE_ARRAY[@]}"; do
      m="$(echo "$m" | xargs)"; [[ -z "$m" ]] && continue
      DEP_LINES+="        <dependency><groupId>vip.mseapp</groupId><artifactId>${m}-service</artifactId><version>${BASE_VERSION}</version></dependency>$NL"
    done
    MQ_DEP=""
    if has_mw rocketmq; then
      MQ_DEP="        <dependency><groupId>vip.mseapp</groupId><artifactId>mq-starter</artifactId><version>${BASE_VERSION}</version></dependency>$NL"
    fi
    cat > "$BUILD/pom.xml" <<EOF
<?xml version="1.0" encoding="UTF-8"?>
<project xmlns="http://maven.apache.org/POM/4.0.0"
         xmlns:xsi="http://www.w3.org/2001/XMLSchema-instance"
         xsi:schemaLocation="http://maven.apache.org/POM/4.0.0 https://maven.apache.org/xsd/maven-4.0.0.xsd">
    <modelVersion>4.0.0</modelVersion>
    <parent>
        <groupId>vip.mseapp</groupId>
        <artifactId>mse-starter-parent</artifactId>
        <version>${BASE_VERSION}</version>
        <relativePath/>
    </parent>
    <artifactId>mse-tenant-standalone</artifactId>
    <dependencies>
        <!-- 私有部署形态入口：service-system/service-auth + starter-bridge + standalone 装配（含导入端点）。
             框架内部模块不经 BOM 仲裁，须显式版本（与 mse-starter-parent 同源升级） -->
        <dependency><groupId>vip.mseapp</groupId><artifactId>tenant-starter</artifactId><version>${BASE_VERSION}</version></dependency>
${MQ_DEP}${DEP_LINES}    </dependencies>
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
EOF
    cat > "$BUILD/src/main/java/vip/mseapp/boot/tenantstandalone/TenantStandaloneApplication.java" <<'EOF'
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
EOF
    (cd "$BUILD" && mvn -q package -DskipTests)
    add_material "build/target/mse-tenant-standalone.jar" artifact mse-monolith ""
    add_summary "租户单体 fat-jar（build/target/mse-tenant-standalone.jar）"
    docker build -f "$REPO/mse-framework/mse-boot/Dockerfile.prebuilt" \
        --build-arg PORT=8080 -t "mse/mse-monolith:$VERSION" "$BUILD" \
      || { echo "collect.sh: 应用镜像构建失败（在线拉基础镜像受限？）——镜像为唯一交付形态，中止"; exit 1; }
    add_image "mse/mse-monolith:$VERSION"
    add_summary "应用镜像 mse/mse-monolith:$VERSION（本机 docker；工作台打包时导出）"
  fi

  # ---- 前端：租户端 dist + 移动基站 + 各模块子应用 → build/ui/html → UI zip ----
  if [[ "$UI" == "true" ]]; then
    echo "==> [hook] 前端：租户端 dist + 移动基站 + 已选模块子应用 → UI zip"
    command -v npm >/dev/null || { echo "collect.sh: 需要 npm（前端构建）"; exit 1; }
    UI_HTML="$BUILD/ui/html"
    UI_SUBS=() UI_MOBILES=()
    # 租户离线交付只含租户端（build:tenant → dist/tenant 放 nginx 根路径）；平台端不进包
    (cd "$REPO/mse-framework/mse-ui" && npm install --no-audit --no-fund && npm run build:tenant)
    rm -rf "$UI_HTML" && mkdir -p "$UI_HTML"
    cp -r "$REPO/mse-framework/mse-ui/dist/tenant/." "$UI_HTML/"
    if [[ -f "$REPO/mse-framework/mse-ui-mobile/package.json" ]]; then
      (cd "$REPO/mse-framework/mse-ui-mobile" && npm install --no-audit --no-fund && npm run build)
      mkdir -p "$UI_HTML/m"
      cp -r "$REPO/mse-framework/mse-ui-mobile/dist/." "$UI_HTML/m/"
      echo "    m/（移动基站前端已收集）"
    fi
    for m in "${MODULE_ARRAY[@]}"; do
      m="$(echo "$m" | xargs)"; [[ -z "$m" ]] && continue
      UI_DIR="$REPO/mse-modules/mse-$m/$m-ui"
      if [[ -d "$UI_DIR" ]]; then
        (cd "$UI_DIR" && npm install --no-audit --no-fund && npm run build)
        mkdir -p "$UI_HTML/sub-apps/$m"
        cp -r "$UI_DIR/dist/." "$UI_HTML/sub-apps/$m/"
        echo "    sub-apps/$m（前端已收集）"
        UI_SUBS+=("$m")
      else
        echo "    跳过模块 $m（未找到 $UI_DIR）"
        add_summary "模块 $m 前端：未找到 $m-ui，PC 子应用未收集"
      fi
      M_UI_DIR="$REPO/mse-modules/mse-$m/$m-mobile-ui"
      if [[ -f "$M_UI_DIR/package.json" ]]; then
        (cd "$M_UI_DIR" && npm install --no-audit --no-fund && npm run build)
        mkdir -p "$UI_HTML/m-apps/$m"
        cp -r "$M_UI_DIR/dist/." "$UI_HTML/m-apps/$m/"
        echo "    m-apps/$m（移动子应用前端已收集）"
        UI_MOBILES+=("$m")
      fi
    done
    UI_LINE="前端物料：基座 dist + 移动基站 m/"
    [[ ${#UI_SUBS[@]} -gt 0 ]] && UI_LINE="$UI_LINE + PC 子应用 ${UI_SUBS[*]}"
    [[ ${#UI_MOBILES[@]} -gt 0 ]] && UI_LINE="$UI_LINE + 移动子应用 ${UI_MOBILES[*]}"
    add_summary "$UI_LINE（UI zip，web 单元应用包）"
    cat > "$UI_HTML/nginx.conf" <<'EOF'
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
EOF
    if [[ "$FORM" == "micro" ]]; then
      sed -i 's|proxy_pass http://app:8080/;|proxy_pass http://gateway:48080/;|' "$UI_HTML/nginx.conf"
    fi
    rm -f "$UI_ZIP"
    "$ZIP_TOOL" zip-dir "$UI_HTML" "$UI_ZIP"
    add_material "mse-ui-$VERSION.zip" artifact mse-monolith ui
    add_summary "UI zip（mse-ui-$VERSION.zip，web 单元应用包）"
  else
    echo "    跳过前端（--ui=false，打包用无前端变体应用 mse-monolith-headless）"
  fi
fi

# ---- 打包请求构造（componentCodes = 勾选中间件 + micro 形态 nacos——勾什么打包什么；未勾 mysql/redis 时套件不含对应组件，DB/Redis 端点经应用安装变量接外部库）----
COMPONENT_JSON=""
add_component() { [[ -n "$COMPONENT_JSON" ]] && COMPONENT_JSON="$COMPONENT_JSON,"; COMPONENT_JSON="$COMPONENT_JSON$(json_str "$1")"; }
if [[ "$FORM" == "micro" ]]; then add_component nacos; fi   # 形态基础设施（非中间件选项），恒入包
for mw in "${MW_ARRAY[@]}"; do
  mw="$(echo "$mw" | xargs)"; [[ -z "$mw" ]] && continue
  add_component "$mw"
done

UNITS_JSON=""
add_unit() { # code source imageRef|materialRole
  [[ -n "$UNITS_JSON" ]] && UNITS_JSON="$UNITS_JSON,"
  local extra=""
  if [[ -n "$2" && "$2" == "image_ref" ]]; then extra="\"imageRef\":$(json_str "$3")"; else extra="\"materialRole\":$(json_str "$3")"; fi
  UNITS_JSON="$UNITS_JSON{\"code\":$(json_str "$1"),\"source\":$(json_str "$2"),$extra}"
}
if [[ "$FORM" == "micro" ]]; then
  APP_CODE="mse-micro"
  for code in gateway system platform auth job; do add_unit "$code" image_ref "mse/mse-$code:$VERSION"; done
  CC_JSON=""
  for cc in mse-micro-common.yaml mse-micro-db.yaml mse-micro-db-biz.yaml mse-micro-dubbo.yaml mse-gateway.yaml mse-system.yaml mse-platform.yaml mse-auth.yaml mse-job.yaml; do
    [[ -n "$CC_JSON" ]] && CC_JSON="$CC_JSON,"; CC_JSON="$CC_JSON$(json_str "$cc")"
  done
  for m in "${MODULE_ARRAY[@]}"; do
    m="$(echo "$m" | xargs)"; [[ -z "$m" ]] && continue
    add_unit "$m" image_ref "mse/mse-$m:$VERSION"
    CC_JSON="$CC_JSON,$(json_str "mse-$m.yaml")"
  done
  [[ "$UI" == "true" ]] && add_unit web artifact ui
else
  if [[ "$UI" == "true" ]]; then APP_CODE="mse-monolith"; else APP_CODE="mse-monolith-headless"; fi
  add_unit app image_ref "mse/mse-monolith:$VERSION"
  [[ "$UI" == "true" ]] && add_unit web artifact ui
fi

CC_SECTION=""
if [[ "$FORM" == "micro" ]]; then
  CC_SECTION="\"configCenterInclude\":[$CC_JSON],"
fi

mkdir -p "$WORK"
cat > "$WORK/build-manifest.json" <<EOF
{
  "appImages":[$APP_IMAGES_JSON],
  "materials":[$MATERIALS_JSON],
  "package": {
    "appCode":$(json_str "$APP_CODE"),
    "units":[$UNITS_JSON],
    "componentCodes":[$COMPONENT_JSON],
    ${CC_SECTION}"buildVars":{}
  },
  "summary":[$SUMMARY_JSON]
}
EOF
echo "==> [hook] 产物合同已写出: $WORK/build-manifest.json"
