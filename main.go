package main

import (
	"context"
	"fmt"
	"net/http"
	"os"
	"os/signal"
	"strings"
	"time"

	"github.com/eryajf/chatgpt-dingtalk/pkg/dingbot"
	"github.com/eryajf/chatgpt-dingtalk/pkg/logger"
	"github.com/eryajf/chatgpt-dingtalk/pkg/process"
	"github.com/eryajf/chatgpt-dingtalk/public"
	"github.com/gin-gonic/gin"
)

func init() {
	public.InitSvc()
	logger.InitLogger(public.Config.LogLevel)
}
func main() {
	Start()
}

func Start() {
	app := gin.Default()
	app.POST("/", func(c *gin.Context) {
		var msgObj dingbot.ReceiveMsg
		err := c.Bind(&msgObj)
		if err != nil {
			return
		}
		// 先校验回调是否合法
		clientId, checkOk := public.CheckRequestWithCredentials(c.GetHeader("timestamp"), c.GetHeader("sign"))
		if !checkOk {
			logger.Warning("该请求不合法，可能是其他企业或者未经允许的应用调用所致，请知悉！")
			return
		}
		// 通过 context 传递 OAuth ClientID，用于后续流程中调用钉钉OpenAPI
		c.Set(public.DingTalkClientIdKeyName, clientId)
		// 为了兼容存量老用户，暂时保留 public.CheckRequest 方法，将来升级到 Stream 模式后，建议去除该方法，采用上面的 CheckRequestWithCredentials
		if !public.CheckRequest(c.GetHeader("timestamp"), c.GetHeader("sign")) && msgObj.SenderStaffId != "" {
			logger.Warning("该请求不合法，可能是其他企业或者未经允许的应用调用所致，请知悉！")
			return
		} else if !public.JudgeOutgoingGroup(msgObj.ConversationID) && msgObj.SenderStaffId == "" {
			logger.Warning("该请求不合法，可能是未经允许的普通群outgoing机器人调用所致，请知悉！")
			return
		}
		// 再校验回调参数是否有价值
		if msgObj.Text.Content == "" || msgObj.ChatbotUserID == "" {
			logger.Warning("从钉钉回调过来的内容为空，根据过往的经验，或许重新创建一下机器人，能解决这个问题")
			return
		}
		// 去除问题的前后空格
		msgObj.Text.Content = strings.TrimSpace(msgObj.Text.Content)
		if public.JudgeSensitiveWord(msgObj.Text.Content) {
			logger.Info(fmt.Sprintf("🙋 %s提问的问题中包含敏感词汇，userid：%#v，消息: %#v", msgObj.SenderNick, msgObj.SenderStaffId, msgObj.Text.Content))
			_, err = msgObj.ReplyToDingtalk(string(dingbot.MARKDOWN), "**🤷 抱歉，您提问的问题中包含敏感词汇，请审核自己的对话内容之后再进行！**")
			if err != nil {
				logger.Warning(fmt.Errorf("send message error: %v", err))
				return
			}
			return
		}
		// 打印钉钉回调过来的请求明细，调试时打开
		logger.Debug(fmt.Sprintf("dingtalk callback parameters: %#v", msgObj))

		if public.Config.ChatType != "0" && msgObj.ConversationType != public.Config.ChatType {
			logger.Info(fmt.Sprintf("🙋 %s使用了禁用的聊天方式", msgObj.SenderNick))
			_, err = msgObj.ReplyToDingtalk(string(dingbot.MARKDOWN), "**🤷 抱歉，管理员禁用了这种聊天方式，请选择其他聊天方式与机器人对话！**")
			if err != nil {
				logger.Warning(fmt.Errorf("send message error: %v", err))
				return
			}
			return
		}

		// 查询群ID，发送指令后，可通过查看日志来获取
		if msgObj.ConversationType == "2" && msgObj.Text.Content == "群ID" {
			if msgObj.RobotCode == "normal" {
				logger.Info(fmt.Sprintf("🙋 outgoing机器人 在『%s』群的ConversationID为: %#v", msgObj.ConversationTitle, msgObj.ConversationID))
			} else {
				logger.Info(fmt.Sprintf("🙋 企业内部机器人 在『%s』群的ConversationID为: %#v", msgObj.ConversationTitle, msgObj.ConversationID))
			}
			//_, err = msgObj.ReplyToDingtalk(string(dingbot.MARKDOWN), msgObj.ConversationID)
			if err != nil {
				logger.Warning(fmt.Errorf("send message error: %v", err))
				return
			}
			return
		}

		// 不在允许群组，不在允许用户（包括在黑名单），满足任一条件，拒绝会话；管理员不受限制
		if msgObj.ConversationType == "2" && !public.JudgeGroup(msgObj.ConversationID) && !public.JudgeAdminUsers(msgObj.SenderStaffId) && msgObj.SenderStaffId != "" {
			logger.Info(fmt.Sprintf("🙋『%s』群组未被验证通过，群ID: %#v，userid：%#v, 昵称: %#v，消息: %#v", msgObj.ConversationTitle, msgObj.ConversationID, msgObj.SenderStaffId, msgObj.SenderNick, msgObj.Text.Content))
			_, err = msgObj.ReplyToDingtalk(string(dingbot.MARKDOWN), "**🤷 抱歉，该群组未被认证通过，无法使用机器人对话功能。**\n>如需继续使用，请联系管理员申请访问权限。")
			if err != nil {
				logger.Warning(fmt.Errorf("send message error: %v", err))
				return
			}
			return
		} else if !public.JudgeUsers(msgObj.SenderStaffId) && !public.JudgeAdminUsers(msgObj.SenderStaffId) && msgObj.SenderStaffId != "" {
			logger.Info(fmt.Sprintf("🙋 %s身份信息未被验证通过，userid：%#v，消息: %#v", msgObj.SenderNick, msgObj.SenderStaffId, msgObj.Text.Content))
			_, err = msgObj.ReplyToDingtalk(string(dingbot.MARKDOWN), "**🤷 抱歉，您的身份信息未被认证通过，无法使用机器人对话功能。**\n>如需继续使用，请联系管理员申请访问权限。")
			if err != nil {
				logger.Warning(fmt.Errorf("send message error: %v", err))
				return
			}
			return
		}
		if len(msgObj.Text.Content) == 0 || msgObj.Text.Content == "帮助" {
			// 欢迎信息
			_, err := msgObj.ReplyToDingtalk(string(dingbot.MARKDOWN), public.Config.Help)
			if err != nil {
				logger.Warning(fmt.Errorf("send message error: %v", err))
				return
			}
		} else {
			logger.Info(fmt.Sprintf("🙋 %s发起的问题: %#v", msgObj.SenderNick, msgObj.Text.Content))
			// 除去帮助之外的逻辑分流在这里处理
			switch {
			case strings.HasPrefix(msgObj.Text.Content, "#图片"):
				err := process.ImageGenerate(c, &msgObj)
				if err != nil {
					logger.Warning(fmt.Errorf("process request: %v", err))
					return
				}
				return
			case strings.HasPrefix(msgObj.Text.Content, "#查对话"):
				err := process.SelectHistory(&msgObj)
				if err != nil {
					logger.Warning(fmt.Errorf("process request: %v", err))
					return
				}
				return
			case strings.HasPrefix(msgObj.Text.Content, "#域名"):
				err := process.DomainMsg(&msgObj)
				if err != nil {
					logger.Warning(fmt.Errorf("process request: %v", err))
					return
				}
				return
			case strings.HasPrefix(msgObj.Text.Content, "#证书"):
				err := process.DomainCertMsg(&msgObj)
				if err != nil {
					logger.Warning(fmt.Errorf("process request: %v", err))
					return
				}
				return
			default:
				msgObj.Text.Content, err = process.GeneratePrompt(msgObj.Text.Content)
				// err不为空：提示词之后没有文本 -> 直接返回提示词所代表的内容
				if err != nil {
					_, err = msgObj.ReplyToDingtalk(string(dingbot.TEXT), msgObj.Text.Content)
					if err != nil {
						logger.Warning(fmt.Errorf("send message error: %v", err))
						return
					}
					return
				}
				err := process.ProcessRequest(&msgObj)
				if err != nil {
					logger.Warning(fmt.Errorf("process request: %v", err))
					return
				}
				return
			}
		}
	})
	// 解析生成后的图片
	app.GET("/images/:filename", func(c *gin.Context) {
		filename := c.Param("filename")
		c.File("./data/images/" + filename)
	})
	// 解析生成后的历史聊天
	app.GET("/history/:filename", func(c *gin.Context) {
		filename := c.Param("filename")
		c.File("./data/chatHistory/" + filename)
	})
	// 直接下载文件
	app.GET("/download/:filename", func(c *gin.Context) {
		filename := c.Param("filename")
		c.Header("Content-Disposition", "attachment; filename="+filename)
		c.Header("Content-Type", "application/octet-stream")
		c.File("./data/chatHistory/" + filename)
	})
	// 服务器健康检测
	app.GET("/", func(c *gin.Context) {
		c.JSON(200, gin.H{
			"status":  "ok",
			"message": "🚀 欢迎使用钉钉机器人 🤖",
		})
	})
	port := ":" + public.Config.Port
	srv := &http.Server{
		Addr:    port,
		Handler: app,
	}

	// Initializing the server in a goroutine so that
	// it won't block the graceful shutdown handling below
	go func() {
		logger.Info("🚀 The HTTP Server is running on", port)
		if err := srv.ListenAndServe(); err != nil && err != http.ErrServerClosed {
			logger.Fatal("listen: %s\n", err)
		}
	}()

	// Wait for interrupt signal to gracefully shutdown the server with
	// a timeout of 5 seconds.
	quit := make(chan os.Signal, 1)
	// kill (no param) default send syscall.SIGTERM
	// kill -2 is syscall.SIGINT
	// kill -9 is syscall.SIGKILL but can't be catch, so don't need add it
	// signal.Notify(quit, syscall.SIGINT, syscall.SIGTERM)
	signal.Notify(quit, os.Interrupt)
	<-quit
	logger.Info("Shutting down server...")

	// 5秒后强制退出
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := srv.Shutdown(ctx); err != nil {
		logger.Fatal("Server forced to shutdown:", err)
	}
	logger.Info("Server exiting!")
}
