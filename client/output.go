package client

// TODO: Rewrite the relevant functions to the n

/*
import (
	"fmt"
	"sort"
)

func TieOutputToTable(input TieOutput, columns []string) TieOutput {
	var result TieOutput = make(map[string]map[string]map[string]bool)

	for keyName, key := range input {
		for _, col := range columns {
			for val, _ := range key[col] {
				if result[keyName][col] == nil {
					if _, found := result[keyName]; found == false {
						result[keyName] = make(map[string]map[string]bool)
					}
					result[keyName][col] = make(map[string]bool, 0)
				}
				result[keyName][col][val] = true
			}
		}
	}
	return result
}
func (table *TieOutput) Clear() {
	*table = make(map[string]map[string]map[string]bool)
}

func (table *TieOutput) Print(columns []string) {
	// Print headers
	for _, col := range columns {
		fmt.Print(col + "\t")
	}
	fmt.Print("\n")
	// Print table
	for keyName, key := range *table {
		fmt.Print(keyName + "\t")
		for i, col := range columns[1:] {
			j := 0
			if len(key[col]) > 1 {
				fmt.Print("[")
			}
			for val, _ := range key[col] {
				if j < len(key[col])-1 {
					fmt.Print(val + ", ")
				} else {
					fmt.Print(val)
				}
				j++
			}
			if len(key[col]) > 1 {
				fmt.Print("]")
			}

			if i != len(columns)-1 {
				fmt.Print("\t")
			}
			// j++
		}
		fmt.Print("\n")
	}
}

type ByFileName [][]string

func (a ByFileName) Len() int           { return len(a[1]) }
func (a ByFileName) Less(i, j int) bool { return a[1][i] < a[1][j] }
func (a ByFileName) Swap(i, j int) {
	for y, _ := range a {
		a[y][i], a[y][j] = a[y][j], a[y][i]
	}
}

func (table *TieOutput) ToSlices(columns []string) [][]string {
	// Print headers
	output := make([][]string, len(columns))
	if len(columns) == 0 {
		return output
	}

	for i, _ := range columns {
		output[i] = make([]string, 0) // len(*table))
	}
	// Print table
	for keyName, key := range *table {
		output[0] = append(output[0], keyName)
		for i, col := range columns[1:] {
			// j := 0
			_, found := key[col]
			if !found {
				output[i+1] = append(output[i+1], "")
				// output[i+1] = ""
				continue
			}
			for val, _ := range key[col] {
				output[i+1] = append(output[i+1], val)
			}
		}
	}
	// sort.Strings(output[1])
	// sort.St

	// for i, x := range output[1] {
	// 	if

	// }
	// sort.SliceStable(output, func(i, j int) bool {
	// 	return output[2][i] < output[2][j]
	// })sort.Sort(ByFileName(output))
	sort.Sort(ByFileName(output))
	return output
}

func LoadTieOutput(key string, value1 string, value2 string, input *TieOutput, columns *[]string) {
	if len(*columns) == 0 {
		*columns = make([]string, 1)
		(*columns)[0] = "keys"
	}
	if *input == nil {
		*input = make(map[string]map[string]map[string]bool)
	}
	found := false
	for _, y := range *columns {
		if y == value1 {
			found = true
			break
		}
	}
	if !found && value1 != "associated" {
		*columns = append(*columns, value1)
	}
	if (*input)[key][value1] == nil {
		if _, found := (*input)[key]; found == false {
			(*input)[key] = make(map[string]map[string]bool)
		}
		(*input)[key][value1] = make(map[string]bool, 0)
	}
	if value1 != "associated" {
		(*input)[key][value1][value2] = true //append(input[key][value1], value2)
	}
}

// func LoadTieOutput2(key string, value1 string, value2 string, input *TieOutput2) {
// 	data := &input.Data
// 	columns := &input.Columns
// 	if len(*columns) == 0 {
// 		*columns = make([]string, 1)
// 		(*columns)[0] = "key"
// 	}
// 	if *data == nil {
// 		*data = make([]request.ReplyGet, 0)
// 	}
// 	found := false
// 	for _, y := range *columns {
// 		if y == value1 {
// 			found = true
// 			break
// 		}
// 	}
// 	if !found && value1 != tiedb.ASSOCIATED {
// 		*columns = append(*columns, value1)
// 	}
// 	found = false
// 	keyPos := -1
// 	for i, y := range *data {
// 		if y.Item == key {
// 			found = true
// 			keyPos = i
// 			break
// 		}
// 	}
// 	if !found {
// 		*data = append(*data, request.ReplyGet{Item: key, Value1: make([]string, 0), Value2: make([]string, 0)})
// 		keyPos = len(*data) - 1
// 	}
// 	// }
// 	// if (*input)[key][value1] == nil {
// 	// 	if _, found := (*input)[key]; found == false {
// 	// 		(*input)[key] = make(map[string]map[string]bool)
// 	// 	}
// 	// 	(*input)[key][value1] = make(map[string]bool, 0)
// 	// }
// 	if value1 != "associated" {
// 		(*data)[keyPos].Value1 = append((*data)[keyPos].Value1, value1)
// 		(*data)[keyPos].Value2 = append((*data)[keyPos].Value2, value2)
// 		// (*input)[key][value1][value2] = true //append(input[key][value1], value2)
// 	}

// 	// func TieOutput2ToTable(input TieOutput, columns []string) TieOutput {
// 	// 	var result TieOutput = make(map[string]map[string]map[string]bool)

// 	// 	for keyName, key := range input {
// 	// 		for _, col := range columns {
// 	// 			for val, _ := range key[col] {
// 	// 				if result[keyName][col] == nil {
// 	// 					if _, found := result[keyName]; found == false {
// 	// 						result[keyName] = make(map[string]map[string]bool)
// 	// 					}
// 	// 					result[keyName][col] = make(map[string]bool, 0)
// 	// 				}
// 	// 				result[keyName][col][val] = true
// 	// 			}
// 	// 		}
// 	// 	}
// 	// 	return result
// }
*/
