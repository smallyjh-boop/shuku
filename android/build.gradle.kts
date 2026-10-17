// 书库安卓 App（手机 app/ + Wear OS 手表 wear/）。两边共用 common/ 里的网络代码。
// 手机和手表的包名、签名必须一样，手表才能从手机拿到连接信息。
plugins {
    id("com.android.application") version "8.7.3" apply false
    id("org.jetbrains.kotlin.android") version "2.1.0" apply false
}
