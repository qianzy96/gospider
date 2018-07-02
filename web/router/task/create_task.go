package task

import (
	"net/http"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/nange/gospider/common"
	"github.com/nange/gospider/spider"
	"github.com/nange/gospider/web/core"
	"github.com/nange/gospider/web/model"
	"github.com/pkg/errors"
	"github.com/sirupsen/logrus"
)

type CreateTaskReq struct {
	model.Task
	OutputSysDBID string `json:"sysdb_id"`
}

type CreateTaskResp struct {
	ID       uint64    `json:"id"`
	CreateAt time.Time `json:"create_at"`
}

func CreateTask(c *gin.Context) {
	var req CreateTaskReq
	if err := c.BindJSON(&req); err != nil {
		logrus.Errorf("bind json failed! err:%+v", err)
		c.Data(http.StatusBadRequest, "", nil)
		return
	}
	logrus.Infof("req:%+v", req)

	intID, err := strconv.Atoi(req.OutputSysDBID)
	if err != nil {
		c.Data(http.StatusBadRequest, "", nil)
		return
	}
	req.Task.OutputSysDBID = uint64(intID)

	rule, config, err := getTaskRuleAndConfig(&req)
	if err != nil {
		logrus.Errorf("getTaskRuleAndConfig failed! err:%+v", err)
		c.Data(http.StatusInternalServerError, "", nil)
		return
	}

	task := req.Task
	task.Status = common.TaskStatusRunning
	if err := task.Create(core.GetDB()); err != nil {
		logrus.Errorf("create task failed! err:%+v", err)
		c.Data(http.StatusInternalServerError, "", nil)
		return
	}

	spiderTask := spider.NewTask(*rule, *config)
	retCh := make(chan common.TaskStatus, 1)
	err = spider.Run(spiderTask, retCh)
	if err != nil {
		logrus.Errorf("spider run task failed! err:%+v", err)
		c.Data(http.StatusInternalServerError, "", nil)
		return
	}

	if task.CronSpec != "" {
		logrus.Infof("starting cron task:%s", task.CronSpec)
		ct, err := spider.NewCronTask(spiderTask, retCh)
		if err != nil {
			logrus.Errorf("new cron task failed! err:%+v", err)
		} else {
			if err := ct.Start(); err != nil {
				logrus.Errorf("start cron task failed! err:%+v", err)
			}
		}

	}

	// TODO: 定时任务停止功能
	go func() {
		for {
			select {
			case status := <-retCh:
				task.Status = status
				if status == common.TaskStatusCompleted {
					task.Counts += 1
				}

				if err := task.Update(core.GetDB(), model.TaskDBSchema.Status, model.TaskDBSchema.Counts); err != nil {
					logrus.Errorf("update task status failed! err:%+v", errors.WithStack(err))
					return
				}

			}
		}
	}()

	c.JSON(http.StatusOK, &CreateTaskResp{
		ID:       task.ID,
		CreateAt: task.CreatedAt,
	})
}

func getTaskRuleAndConfig(req *CreateTaskReq) (*spider.TaskRule, *spider.TaskConfig, error) {
	rule, err := spider.GetTaskRule(req.TaskRuleName)
	if err != nil {
		return nil, nil, err
	}

	var optAllowedDomains []string
	if req.OptAllowedDomains != "" {
		optAllowedDomains = strings.Split(req.OptAllowedDomains, ",")
	}
	var urlFiltersReg []*regexp.Regexp
	if req.OptURLFilters != "" {
		urlFilters := strings.Split(req.OptURLFilters, ",")
		for _, v := range urlFilters {
			reg, err := regexp.Compile(v)
			if err != nil {
				return nil, nil, errors.WithStack(err)
			}
			urlFiltersReg = append(urlFiltersReg, reg)
		}
	}

	sdb := model.SysDB{}
	query := model.NewSysDBQuerySet(core.GetDB())
	if err := query.IDEq(req.Task.OutputSysDBID).One(&sdb); err != nil {
		logrus.Errorf("query sysdb err: %+v, id:%d", err, req.OutputSysDBID)
		return nil, nil, errors.WithStack(err)
	}

	config := &spider.TaskConfig{
		CronSpec: req.CronSpec,
		Option: spider.Option{
			UserAgent:              req.OptUserAgent,
			MaxDepth:               req.OptMaxDepth,
			AllowedDomains:         optAllowedDomains,
			URLFilters:             urlFiltersReg,
			AllowURLRevisit:        rule.AllowURLRevisit,
			MaxBodySize:            req.OptMaxBodySize,
			IgnoreRobotsTxt:        rule.IgnoreRobotsTxt,
			ParseHTTPErrorResponse: rule.ParseHTTPErrorResponse,
			DisableCookies:         rule.DisableCookies,
		},
		Limit: spider.Limit{
			Enable:      req.LimitEnable,
			DomainGlob:  req.LimitDomainGlob,
			Delay:       time.Duration(req.LimitDelay) * time.Millisecond,
			RandomDelay: time.Duration(req.LimitRandomDelay) * time.Millisecond,
			Parallelism: req.LimitParallelism,
		},
		OutputConfig: spider.OutputConfig{
			Type: req.OutputType,
			MySQLConf: spider.MySQLConf{
				Host:     sdb.Host,
				Port:     sdb.Port,
				User:     sdb.User,
				Password: sdb.Password,
				DBName:   sdb.DBName,
			},
		},
	}

	return rule, config, nil
}
