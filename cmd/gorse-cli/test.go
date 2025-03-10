// Copyright 2020 gorse Project Authors
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
// http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.
package main

import (
	"fmt"
	"os"
	"runtime"
	"strconv"
	"strings"
	"time"

	"github.com/olekukonko/tablewriter"
	log "github.com/sirupsen/logrus"
	"github.com/spf13/cobra"
	"github.com/zhenghaoz/gorse/model"
	"github.com/zhenghaoz/gorse/model/cf"
	"github.com/zhenghaoz/gorse/model/rank"
	"github.com/zhenghaoz/gorse/storage/data"
)

func init() {
	cliCommand.AddCommand(testCommand)
	// test match model
	testCommand.AddCommand(testMatchCommand)
	testMatchCommand.PersistentFlags().String("feedback-type", "", "Set feedback type.")
	testMatchCommand.PersistentFlags().String("load-builtin", "", "load data from built-in")
	testMatchCommand.PersistentFlags().String("load-csv", "", "load data from CSV file")
	testMatchCommand.PersistentFlags().String("csv-sep", "\t", "load CSV file with separator")
	testMatchCommand.PersistentFlags().String("csv-format", "", "load CSV file with header")
	testMatchCommand.PersistentFlags().Bool("csv-header", false, "load CSV file with header")
	testMatchCommand.PersistentFlags().Int("verbose", 1, "Verbose period")
	testMatchCommand.PersistentFlags().Int("jobs", runtime.NumCPU(), "Number of jobs for model fitting")
	testMatchCommand.PersistentFlags().Int("top-k", 10, "Length of recommendation list")
	testMatchCommand.PersistentFlags().Int("n-negatives", 100, "Number of negative samples")
	testMatchCommand.PersistentFlags().Int("n-test-users", 0, "Number of users for sampled test set")
	for _, paramFlag := range matchParamFlags {
		testMatchCommand.PersistentFlags().String(paramFlag.Name, "", paramFlag.Help)
	}
	// test rank model
	testCommand.AddCommand(testRankCommand)
	testRankCommand.PersistentFlags().Int64("seed", 0, "Rand seed.")
	testRankCommand.PersistentFlags().Float32("test-ratio", 0.2, "Test ratio.")
	testRankCommand.PersistentFlags().String("load-builtin", "", "load data from built-in")
	testRankCommand.PersistentFlags().String("task", "r", "Task for ranking (c - classification, r - regression)")
	testRankCommand.PersistentFlags().Int("verbose", 1, "Verbose period")
	testRankCommand.PersistentFlags().Int("jobs", runtime.NumCPU(), "Number of jobs for model fitting")
	for _, paramFlag := range rankParamFlags {
		testRankCommand.PersistentFlags().String(paramFlag.Name, "", paramFlag.Help)
	}
}

/* Models */

/* Flags for parameters */

const (
	intFlag     = 0
	float64Flag = 1
)

type paramFlag struct {
	Type int
	Key  model.ParamName
	Name string
	Help string
}

var matchParamFlags = []paramFlag{
	{float64Flag, model.Lr, "lr", "Learning rate"},
	{float64Flag, model.Reg, "reg", "Regularization strength"},
	{intFlag, model.NEpochs, "n-epochs", "Number of epochs"},
	{intFlag, model.NFactors, "n-factors", "Number of factors"},
	{float64Flag, model.InitMean, "init-mean", "Mean of gaussian initial parameters"},
	{float64Flag, model.InitStdDev, "init-std", "Standard deviation of gaussian initial parameters"},
	{float64Flag, model.Alpha, "neg-weight", "Alpha of negative samples in ALS."},
}

var rankParamFlags = []paramFlag{
	{float64Flag, model.Lr, "lr", "Learning rate"},
	{float64Flag, model.Reg, "reg", "Regularization strength"},
	{intFlag, model.NEpochs, "n-epochs", "Number of epochs"},
	{intFlag, model.NFactors, "n-factors", "Number of factors"},
	{float64Flag, model.InitMean, "init-mean", "Mean of gaussian initial parameters"},
	{float64Flag, model.InitStdDev, "init-std", "Standard deviation of gaussian initial parameters"},
}

func parseParamFlags(cmd *cobra.Command) model.ParamsGrid {
	grid := make(model.ParamsGrid)
	for _, paramFlag := range matchParamFlags {
		if cmd.PersistentFlags().Changed(paramFlag.Name) {
			text, err := cmd.PersistentFlags().GetString(paramFlag.Name)
			if err != nil {
				log.Fatalf("cli: failed to get arguments (%v)", err)
			}
			grid[paramFlag.Key] = parseParamList(text, paramFlag.Type)
		}
	}
	return grid
}

func parseParamList(text string, tp int) []interface{} {
	if text == "" {
		log.Fatal("cli: empty string for param list")
	}
	if text[0] == '[' && text[len(text)-1] == ']' {
		text = text[1 : len(text)-1]
	}
	paramTexts := strings.Split(text, ",")
	params := make([]interface{}, len(paramTexts))
	for i, paramText := range paramTexts {
		params[i] = parseParam(paramText, tp)
	}
	return params
}

func parseParam(text string, tp int) interface{} {
	switch tp {
	case intFlag:
		i, err := strconv.Atoi(text)
		if err != nil {
			log.Fatalf("cli: failed to parse param (%v)", err)
		}
		return i
	case float64Flag:
		f, err := strconv.ParseFloat(text, 64)
		if err != nil {
			log.Fatalf("cli: failed to parse param (%v)", err)
		}
		return f
	default:
		log.Fatal("cli: unknown parameter type", tp)
		return nil
	}
}

var testCommand = &cobra.Command{
	Use:   "test",
	Short: "test recommendation model",
}

var testMatchCommand = &cobra.Command{
	Use:   "match",
	Short: "Test match model by user-leave-one-out",
	Args:  cobra.ExactArgs(1),
	Run: func(cmd *cobra.Command, args []string) {
		modelName := args[0]
		m, err := cf.NewModel(modelName, nil)
		if err != nil {
			log.Fatalf("cli: falied to create model (%v)", modelName)
		}
		// Load data
		var trainSet, testSet *cf.DataSet
		if cmd.PersistentFlags().Changed("load-builtin") {
			name, _ := cmd.PersistentFlags().GetString("load-builtin")
			log.Infof("Load built-in dataset %s\n", name)
			trainSet, testSet, err = cf.LoadDataFromBuiltIn(name)
			if err != nil {
				log.Fatal("cli: ", err)
			}
		} else if cmd.PersistentFlags().Changed("load-csv") {
			name, _ := cmd.PersistentFlags().GetString("load-csv")
			sep, _ := cmd.PersistentFlags().GetString("csv-sep")
			header, _ := cmd.PersistentFlags().GetBool("csv-header")
			numTestUsers, _ := cmd.PersistentFlags().GetInt("n-test-users")
			seed, _ := cmd.PersistentFlags().GetInt("random-state")
			log.Infof("Load csv file %v", name)
			data := cf.LoadDataFromCSV(name, sep, header)
			trainSet, testSet = data.Split(numTestUsers, int64(seed))
		} else {
			feedbackType, _ := cmd.PersistentFlags().GetString("feedback-type")
			numTestUsers, _ := cmd.PersistentFlags().GetInt("n-test-users")
			seed, _ := cmd.PersistentFlags().GetInt("random-state")
			// Open database
			database, err := data.Open(globalConfig.Database.DataStore)
			if err != nil {
				log.Fatalf("cli: failed to connect database (%v)", err)
			}
			defer database.Close()
			// Load data
			log.Infof("Load data from database")
			data, _, err := cf.LoadDataFromDatabase(database, []string{feedbackType})
			if err != nil {
				log.Fatalf("cli: failed to load data from database (%v)", err)
			}
			if data.Count() == 0 {
				log.Fatalf("cli: empty dataset")
			}
			log.Infof("data set: #user = %v, #item = %v, #feedback = %v", data.UserCount(), data.ItemCount(), data.Count())
			trainSet, testSet = data.Split(numTestUsers, int64(seed))
		}
		log.Infof("train set: #user = %v, #item = %v, #feedback = %v", trainSet.UserCount(), trainSet.ItemCount(), trainSet.Count())
		log.Infof("test set: #user = %v, #item = %v, #feedback = %v", testSet.UserCount(), testSet.ItemCount(), testSet.Count())
		// Load hyper-parameters
		grid := parseParamFlags(cmd)
		log.Printf("Load hyper-parameters grid: %v\n", grid)
		// Load runtime options
		fitConfig := &cf.FitConfig{}
		fitConfig.Verbose, _ = cmd.PersistentFlags().GetInt("verbose")
		fitConfig.Jobs, _ = cmd.PersistentFlags().GetInt("jobs")
		fitConfig.TopK, _ = cmd.PersistentFlags().GetInt("top-k")
		fitConfig.Candidates, _ = cmd.PersistentFlags().GetInt("n-negatives")
		// Cross validation
		start := time.Now()
		var result *cf.ParamsSearchResult
		if grid.Len() == 0 {
			result = cf.NewParamsSearchResult()
			score := m.Fit(trainSet, testSet, fitConfig)
			result.AddScore(nil, score)
		} else {
			a := cf.GridSearchCV(m, trainSet, testSet, grid, 0, fitConfig)
			result = &a
		}
		elapsed := time.Since(start)
		// Render table
		table := tablewriter.NewWriter(os.Stdout)
		table.SetHeader([]string{"#", "NDCG@10", "Precision@10", "Recall@10", "Params"})
		for i := range result.Params {
			score := result.Scores[i]
			table.Append([]string{
				fmt.Sprintf("%v", i),
				fmt.Sprintf("%v", score.NDCG),
				fmt.Sprintf("%v", score.Precision),
				fmt.Sprintf("%v", score.Recall),
				fmt.Sprintf("%v", result.Params[i].ToString()),
			})
		}
		table.Render()
		log.Printf("Complete cross validation (%v)\n", elapsed)
	},
}

var testRankCommand = &cobra.Command{
	Use:   "rank",
	Short: "Test rank model.",
	Run: func(cmd *cobra.Command, args []string) {
		var err error
		// Load data
		var trainSet, testSet *rank.Dataset
		if cmd.PersistentFlags().Changed("load-builtin") {
			name, _ := cmd.PersistentFlags().GetString("load-builtin")
			log.Infof("Load built-in dataset %s\n", name)
			trainSet, testSet, err = rank.LoadDataFromBuiltIn(name)
			if err != nil {
				log.Fatal("cli: ", err)
			}
		} else {
			// load dataset
			feedbackType, _ := cmd.PersistentFlags().GetString("feedback-type")
			// Open database
			database, err := data.Open(globalConfig.Database.DataStore)
			if err != nil {
				log.Fatalf("cli: failed to connect database (%v)", err)
			}
			defer database.Close()
			seed, _ := cmd.PersistentFlags().GetInt64("seed")
			testRatio, _ := cmd.PersistentFlags().GetFloat32("test-ratio")
			log.Infof("Load data from database")
			dataSet, err := rank.LoadDataFromDatabase(database, []string{feedbackType})
			if err != nil {
				log.Fatalf("cli: failed to load data from database (%v)", err)
			}
			if dataSet.PositiveCount == 0 {
				log.Fatalf("cli: empty dataset")
			}
			log.Infof("data set: #user = %v, #item = %v, #positive = %v",
				dataSet.UserCount(), dataSet.ItemCount(), dataSet.PositiveCount)
			trainSet, testSet = dataSet.Split(testRatio, seed)
			testSet.NegativeSample(1, trainSet, 0)
		}
		log.Infof("train set: #user = %v, #item = %v, #positive = %v", trainSet.UserCount(), trainSet.ItemCount(), trainSet.PositiveCount)
		log.Infof("test set: #user = %v, #item = %v, #positive = %v", testSet.UserCount(), testSet.ItemCount(), testSet.PositiveCount)
		// Load hyper-parameters
		grid := parseParamFlags(cmd)
		log.Printf("Load hyper-parameters grid: %v\n", grid)
		// Load runtime options
		fitConfig := &rank.FitConfig{}
		fitConfig.Verbose, _ = cmd.PersistentFlags().GetInt("verbose")
		fitConfig.Jobs, _ = cmd.PersistentFlags().GetInt("jobs")
		// Cross validation
		start := time.Now()
		task, _ := cmd.PersistentFlags().GetString("task")
		m := rank.NewFM(rank.FMTask(task), nil)
		var result *rank.ParamsSearchResult
		if grid.Len() == 0 {
			result = rank.NewParamsSearchResult()
			score := m.Fit(trainSet, testSet, fitConfig)
			result.AddScore(nil, score)
		} else {
			a := rank.GridSearchCV(m, trainSet, testSet, grid, 0, fitConfig)
			result = &a
		}
		elapsed := time.Since(start)
		// Render table
		table := tablewriter.NewWriter(os.Stdout)
		table.SetHeader([]string{"#", result.BestScore.GetName(), "Params"})
		for i := range result.Params {
			score := result.Scores[i]
			table.Append([]string{
				fmt.Sprintf("%v", i),
				fmt.Sprintf("%v", score.GetValue()),
				fmt.Sprintf("%v", result.Params[i].ToString()),
			})
		}
		table.Render()
		log.Printf("Complete cross validation (%v)\n", elapsed)
	},
}
