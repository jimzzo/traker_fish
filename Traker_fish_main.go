package main

import (
	"fmt"
	"log"
	"net/http"
	"strings"

	"github.com/PuerkitoBio/goquery"
)

func consultarExistenciasHandler(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "Método no permitido", http.StatusMethodNotAllowed)
		return
	}

	err := r.ParseForm()
	if err != nil {
		http.Error(w, "Error al parsear datos", http.StatusBadRequest)
		return
	}

	entradaUsuario := r.FormValue("pez")
	if entradaUsuario == "" {
		http.Error(w, "Falta el nombre del pez", http.StatusBadRequest)
		return
	}

	partes := strings.Split(entradaUsuario, ":")
	pezBase := strings.TrimSpace(partes[0])
	var varianteBuscada string
	if len(partes) > 1 {
		varianteBuscada = strings.TrimSpace(partes[1])
	}

	urlBase := "https://reef.xs-pets.com/fish"
	client := &http.Client{}
	req, _ := http.NewRequest("GET", urlBase, nil)
	req.Header.Set("User-Agent", "Mozilla/5.0")

	resp, err := client.Do(req)
	if err != nil || resp.StatusCode != 200 {
		http.Error(w, "Error al conectar con Xundra", http.StatusInternalServerError)
		return
	}
	defer resp.Body.Close()

	doc, err := goquery.NewDocumentFromReader(resp.Body)
	if err != nil {
		http.Error(w, "Error leyendo HTML", http.StatusInternalServerError)
		return
	}

	var linkDetalle string
	doc.Find("a").Each(func(i int, s *goquery.Selection) {
		if strings.EqualFold(strings.TrimSpace(s.Text()), pezBase) {
			if href, existe := s.Attr("href"); existe {
				linkDetalle = href
			}
		}
	})

	if linkDetalle == "" {
		w.Write([]byte(fmt.Sprintf("Pez [%s] no encontrado.", pezBase)))
		return
	}

	if !strings.HasPrefix(linkDetalle, "http") {
		linkDetalle = "https://reef.xs-pets.com/" + strings.TrimPrefix(linkDetalle, "/")
	}

	reqDetalle, _ := http.NewRequest("GET", linkDetalle, nil)
	reqDetalle.Header.Set("User-Agent", "Mozilla/5.0")
	respDetalle, err := client.Do(reqDetalle)
	if err != nil {
		http.Error(w, "Error al entrar al detalle", http.StatusInternalServerError)
		return
	}
	defer respDetalle.Body.Close()

	docDetalle, err := goquery.NewDocumentFromReader(respDetalle.Body)
	if err != nil {
		http.Error(w, "Error leyendo detalle", http.StatusInternalServerError)
		return
	}

	var resultadoBuilder strings.Builder
	encontrado := false

	// Buscamos en las filas de las tablas del detalle extrayendo las celdas individualmente
	docDetalle.Find("tr").Each(func(i int, s *goquery.Selection) {
		var celdas []string
		s.Find("td").Each(func(j int, cell *goquery.Selection) {
			celdas = append(celdas, strings.TrimSpace(cell.Text()))
		})

		if len(celdas) >= 3 {
			nombreFila := celdas[0]
			eggs := celdas[1]
			fish := celdas[2]

			// Si coincide con la variante específica o si queremos mostrar todas las variantes del pez
			if varianteBuscada == "" || strings.Contains(strings.ToLower(nombreFila), strings.ToLower(varianteBuscada)) {
				resultadoBuilder.WriteString(fmt.Sprintf("• %s EGGS: %s | FISH: %s \n", nombreFila, eggs, fish))
				encontrado = true
			}
		}
	})

	mensajeFinal := resultadoBuilder.String()
	if !encontrado {
		mensajeFinal = fmt.Sprintf("Sin stock o datos para: %s", entradaUsuario)
	}

	w.Write([]byte(mensajeFinal))
}

func main() {
	http.HandleFunc("/api/pez", consultarExistenciasHandler)
	fmt.Println("Servidor Go activo en puerto 8080...")
	log.Fatal(http.ListenAndServe(":8080", nil))
}
