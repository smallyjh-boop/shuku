package app.shuku.core

import java.io.ByteArrayInputStream
import java.io.ByteArrayOutputStream
import java.io.DataInputStream
import java.io.DataOutputStream

/**
 * 手表经手机转发书库请求（手表自己连不上书库时，比如出门、没有 Tailscale）。
 * 手表在本机 127.0.0.1 起一个小服务，把请求通过 Wear 数据层发给手机；手机用它自己的连接去书库取，再分块传回手表。
 *   /shuku/relay/req   手表 → 手机：JSON {id, m, p, h, b}（b 是 base64 的请求体，没有就是空）
 *   /shuku/relay/res   手机 → 手表：一块返回数据，格式见 encodeChunk
 */
object Relay {
    const val PATH_REQ = "/shuku/relay/req"
    const val PATH_RES = "/shuku/relay/res"

    /** 每块不超过这么多字节（Wear 消息上限约 100KB，留出余量） */
    const val CHUNK = 48 * 1024

    class Chunk(val id: String, val index: Int, val total: Int, val status: Int, val type: String, val data: ByteArray)

    fun encodeChunk(id: String, index: Int, total: Int, status: Int, type: String, data: ByteArray): ByteArray {
        val bo = ByteArrayOutputStream()
        DataOutputStream(bo).use { o ->
            o.writeUTF(id)
            o.writeInt(index)
            o.writeInt(total)
            o.writeInt(status)
            o.writeUTF(type)
            o.writeInt(data.size)
            o.write(data)
        }
        return bo.toByteArray()
    }

    fun decodeChunk(bytes: ByteArray): Chunk {
        DataInputStream(ByteArrayInputStream(bytes)).use { i ->
            val id = i.readUTF()
            val index = i.readInt()
            val total = i.readInt()
            val status = i.readInt()
            val type = i.readUTF()
            val n = i.readInt()
            val data = ByteArray(n)
            i.readFully(data)
            return Chunk(id, index, total, status, type, data)
        }
    }
}
